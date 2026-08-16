#!/usr/bin/env ruby
# frozen_string_literal: true

require "open3"
require "tempfile"
require "yaml"

WORKFLOW_PATH = File.expand_path("../.github/workflows/ci.yml", __dir__)
COMPOSE_PATH = File.expand_path("../deploy/compose.example.yaml", __dir__)
COMPOSE_ENV_PATH = File.expand_path("../.env.example", __dir__)
WORKFLOW = YAML.safe_load(File.read(WORKFLOW_PATH), aliases: true)
compose_available = system("docker", "compose", "version", out: File::NULL, err: File::NULL)
raise "docker compose is required to validate deploy/compose.example.yaml" unless compose_available
compose_output, compose_status = Open3.capture2e("docker", "compose", "--env-file", COMPOSE_ENV_PATH, "-f", COMPOSE_PATH, "config")
raise "render example Compose: #{compose_output}" unless compose_status.success?
COMPOSE = YAML.safe_load(compose_output, permitted_classes: [Symbol], aliases: true)
JOBS = WORKFLOW.fetch("jobs")
RELEASE_JOBS = %w[container-backend container-frontend publish-backend publish-frontend promote-backend promote-frontend].freeze
REGISTRY_JOBS = %w[publish-backend publish-frontend promote-backend promote-frontend].freeze
SECRET_GATED_JOBS = %w[policy go frontend container-backend container-frontend].freeze
JOB_TIMEOUTS = {
  "secret-scan" => 15,
  "policy" => 10,
  "go" => 15,
  "frontend" => 15,
  "container-backend" => 45,
  "container-frontend" => 45,
  "publish-backend" => 20,
  "publish-frontend" => 20,
  "promote-backend" => 15,
  "promote-frontend" => 15
}.freeze

def assert(condition, message)
  raise message unless condition
end

def steps(job_name)
  JOBS.fetch(job_name).fetch("steps")
end

def action_steps(job_name, action)
  steps(job_name).select { |step| step.fetch("uses", "").start_with?(action) }
end

def run_text(job_name)
  steps(job_name).map { |step| step["run"] }.compact.join("\n")
end

workflow_text = File.read(WORKFLOW_PATH)
assert(workflow_text.match?(/^\s*push:\s*$/), "push trigger is missing")
assert(workflow_text.match?(/^\s*workflow_dispatch:\s*$/), "workflow_dispatch trigger is missing")
assert(!workflow_text.match?(/^\s*pull_request:\s*$/), "pull_request trigger must stay disabled for the push-only workflow")
assert(WORKFLOW.fetch("concurrency").fetch("cancel-in-progress") == true, "concurrency must cancel stale runs")
concurrency_group = WORKFLOW.fetch("concurrency").fetch("group")
assert(concurrency_group.include?("github.event_name") && concurrency_group.include?("github.ref"), "concurrency must be scoped by event and ref")
assert(!workflow_text.include?("setup-qemu-action"), "QEMU must not be used")

assert(JOBS.key?("secret-scan"), "missing job: secret-scan")
secret_scan = JOBS.fetch("secret-scan")
secret_checkout = secret_scan.fetch("steps").find { |step| step.fetch("uses", "").start_with?("actions/checkout@") }
assert(secret_checkout, "secret-scan must check out the repository")
assert(secret_checkout.fetch("with").fetch("fetch-depth") == 0, "secret-scan must fetch complete history")
assert(secret_checkout.fetch("with").fetch("persist-credentials") == false, "secret-scan checkout must not persist credentials")
assert(run_text("secret-scan").include?("scripts/scan-repository-secrets.sh"), "secret-scan must scan the current tree and Git history")

secret_scanner_path = File.expand_path("scan-repository-secrets.sh", __dir__)
assert(File.exist?(secret_scanner_path), "missing script: scripts/scan-repository-secrets.sh")
secret_scanner = File.read(secret_scanner_path)
assert(secret_scanner.match?(/gitleaks:[^\s"]+@sha256:[0-9a-f]{64}/), "Gitleaks image must be pinned by tag and digest")
%w[--network\ none --read-only --cap-drop\ ALL --security-opt\ no-new-privileges --redact=100].each do |option|
  assert(secret_scanner.include?(option.tr("\\", "")), "secret scanner is missing container hardening option: #{option.tr("\\", "")}")
end
assert(secret_scanner.match?(/git\b[^\n]*\barchive\b/) && secret_scanner.include?("--no-git"), "secret scanner must scan the current tracked tree")
assert(secret_scanner.include?("--source=/repo") && secret_scanner.include?("--log-opts=--all"), "secret scanner must scan complete Git history across all refs")

SECRET_GATED_JOBS.each do |job_name|
  assert(Array(JOBS.fetch(job_name)["needs"]).include?("secret-scan"), "#{job_name} must wait for secret-scan")
end

assert(JOBS.keys.sort == JOB_TIMEOUTS.keys.sort, "job timeout policy does not cover every workflow job")
JOB_TIMEOUTS.each do |job_name, timeout|
  assert(JOBS.fetch(job_name).fetch("timeout-minutes") == timeout, "#{job_name} timeout must be #{timeout} minutes")
end

RELEASE_JOBS.each { |job_name| assert(JOBS.key?(job_name), "missing job: #{job_name}") }

%w[backend frontend].each do |component|
  build_job = "container-#{component}"
  publish_job = "publish-#{component}"
  promote_job = "promote-#{component}"

  runner = JOBS.fetch(build_job).fetch("runs-on")
  assert(runner.include?("ubuntu-24.04-arm") && runner.include?("ubuntu-latest"), "#{build_job} must select native ARM or amd64 runners")
  assert(!Array(JOBS.fetch(build_job)["needs"]).any? { |need| need.start_with?("container-") }, "#{build_job} must run independently from the other image")

  build_steps = action_steps(build_job, "docker/build-push-action@")
  assert(build_steps.length == 1, "#{build_job} must build the release image exactly once")
  build_options = build_steps.first.fetch("with")
  assert(build_options.fetch("platforms").include?("linux/arm64") && build_options.fetch("platforms").include?("linux/amd64"), "#{build_job} must select an explicit platform")
  assert(build_options.fetch("outputs").include?("type=docker") && build_options.fetch("outputs").include?(".tar"), "#{build_job} must output a Docker archive")
  assert(build_options.fetch("tags").include?("github.run_id") && build_options.fetch("tags").include?("github.run_attempt"), "#{build_job} candidate tags must be unique per run attempt")
  assert(build_options.fetch("cache-from").end_with?("'type=local,src=/tmp/buildx-cache' || '' }}"), "#{build_job} must restore its local BuildKit cache")
  assert(build_options.fetch("cache-to") == "type=local,dest=/tmp/buildx-cache-new,mode=max", "#{build_job} must export a complete local cache for later approval")

  cache_restores = action_steps(build_job, "actions/cache/restore@")
  assert(cache_restores.length == 1, "#{build_job} must restore one GitHub Actions cache")
  cache_restore = cache_restores.first
  assert(cache_restore.fetch("id") == "restore-build-cache", "#{build_job} cache restore must expose whether a compatible cache exists")
  assert(cache_restore.fetch("uses").end_with?("@55cc8345863c7cc4c66a329aec7e433d2d1c52a9"), "#{build_job} must pin actions/cache v6.1.0")
  restore_options = cache_restore.fetch("with")
  cache_prefix = "${{ runner.os }}-buildx-${{ runner.arch }}-#{component}-"
  assert(restore_options.fetch("path") == "/tmp/buildx-cache", "#{build_job} must restore the cache consumed by BuildKit")
  assert(restore_options.fetch("key").start_with?(cache_prefix) && restore_options.fetch("key").include?("github.sha"), "#{build_job} cache keys must be isolated by OS, architecture, component, and commit")
  assert(restore_options.fetch("restore-keys").strip == cache_prefix, "#{build_job} must restore the latest compatible cache")
  assert(steps(build_job).index(cache_restore) < steps(build_job).index(build_steps.first), "#{build_job} must restore cache before building")
  assert(build_options.fetch("cache-from").include?("steps.restore-build-cache.outputs.cache-matched-key"), "#{build_job} must import local cache only after a successful restore")

  trivy_steps = action_steps(build_job, "aquasecurity/trivy-action@")
  assert(trivy_steps.length == 2, "#{build_job} must use focused secret and vulnerability/configuration gates")
  assert(trivy_steps.all? { |step| step.fetch("uses").end_with?("@ed142fd0673e97e23eac54620cfb913e5ce36c25") }, "#{build_job} must pin Trivy v0.36.0")
  secret_scan = trivy_steps.find { |step| step.fetch("with").fetch("scanners") == "secret" }
  assert(secret_scan, "#{build_job} must scan all secrets independently from severity filtering")
  secret_options = secret_scan.fetch("with")
  assert(!secret_options.key?("severity"), "#{build_job} secret scanning must not apply vulnerability severity filtering")
  assert(secret_options.fetch("output") == "/dev/null", "#{build_job} must not print secret findings to workflow logs")
  assert(secret_options.fetch("hide-progress") == true, "#{build_job} secret scanning progress must stay hidden")
  assert(secret_scan.fetch("env").fetch("TRIVY_IMAGE_CONFIG_SCANNERS") == "secret", "#{build_job} must inspect image configuration secrets")
  security_scan = trivy_steps.find { |step| step.fetch("with").fetch("scanners").split(",").sort == %w[misconfig vuln] }
  assert(security_scan, "#{build_job} must scan vulnerabilities and misconfigurations")
  security_options = security_scan.fetch("with")
  assert(security_options.fetch("severity") == "HIGH,CRITICAL", "#{build_job} must fail on HIGH and CRITICAL findings")
  assert(security_options.fetch("ignore-unfixed") == true, "#{build_job} must gate fixable vulnerabilities")
  assert(security_scan.fetch("env").fetch("TRIVY_IMAGE_CONFIG_SCANNERS") == "misconfig", "#{build_job} must inspect image configuration misconfigurations")
  trivy_steps.each do |step|
    trivy_options = step.fetch("with")
    assert(trivy_options.fetch("input").end_with?(".tar"), "#{build_job} must scan the saved archive")
    assert(trivy_options.fetch("exit-code").to_s == "1", "#{build_job} must fail closed")
  end

  cache_saves = action_steps(build_job, "actions/cache/save@")
  assert(cache_saves.length == 1, "#{build_job} must save one approved GitHub Actions cache")
  cache_save = cache_saves.first
  assert(cache_save.fetch("uses").end_with?("@55cc8345863c7cc4c66a329aec7e433d2d1c52a9"), "#{build_job} must pin actions/cache v6.1.0")
  save_options = cache_save.fetch("with")
  assert(save_options.fetch("path") == "/tmp/buildx-cache", "#{build_job} must save the cache under its stable restore path")
  assert(save_options.fetch("key") == restore_options.fetch("key"), "#{build_job} must save the cache under its restore key")
  cache_rotation = steps(build_job).find { |step| step.fetch("run", "").include?("mv /tmp/buildx-cache-new /tmp/buildx-cache") }
  assert(cache_rotation, "#{build_job} must rotate the approved cache into its stable restore path")
  rotation_text = cache_rotation.fetch("run")
  assert(rotation_text.include?("rm -rf /tmp/buildx-cache"), "#{build_job} must remove the restored cache before rotation")
  rotation_step_index = steps(build_job).index(cache_rotation)
  save_step_index = steps(build_job).index(cache_save)
  assert(trivy_steps.all? { |step| steps(build_job).index(step) < rotation_step_index }, "#{build_job} must approve cache rotation only after all image scans pass")
  assert(rotation_step_index < save_step_index, "#{build_job} must rotate the approved cache before persisting it")

  build_runs = run_text(build_job)
  assert(build_runs.include?("sha256sum") && build_runs.include?(".tar.sha256"), "#{build_job} must checksum the archive")
  uploads = action_steps(build_job, "actions/upload-artifact@")
  assert(uploads.length == 1, "#{build_job} must upload one archive artifact")
  assert(uploads.first.fetch("uses").end_with?("@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"), "#{build_job} must pin upload-artifact v7.0.1")
  upload_options = uploads.first.fetch("with")
  assert(upload_options.fetch("name").include?("github.run_id") && upload_options.fetch("name").include?("github.run_attempt"), "#{build_job} artifact names must be unique per run attempt")
  assert(upload_options.fetch("retention-days").to_i == 1, "#{build_job} artifacts must expire after one day")
  assert(upload_options.fetch("compression-level").to_i == 0, "#{build_job} must not recompress image archives")
  assert(upload_options.fetch("path").include?(".tar") && upload_options.fetch("path").include?(".tar.sha256"), "#{build_job} artifact must contain only the archive and checksum")

  publish = JOBS.fetch(publish_job)
  assert(Array(publish.fetch("needs")).include?(build_job), "#{publish_job} must consume #{build_job}")
  publish_condition = publish.fetch("if")
  assert(publish_condition.include?("push") && publish_condition.include?("workflow_dispatch") && publish_condition.include?("refs/heads/main"), "#{publish_job} must publish automatic amd64 and manual ARM only from main")
  publish_runs = run_text(publish_job)
  assert(publish_runs.include?("sha256sum --check") && publish_runs.include?("docker load"), "#{publish_job} must verify and load the scanned archive")
  assert(publish_runs.include?("GITHUB_RUN_ID") && publish_runs.include?("GITHUB_RUN_ATTEMPT"), "#{publish_job} must publish only a run-specific candidate tag")
  assert(publish_runs.include?("^sha256:[0-9a-f]{64}$"), "#{publish_job} must strictly validate the registry digest")
  assert(component == "backend" || publish_runs.include?("hookfly-frontend"), "#{publish_job} image name is missing")
  assert(action_steps(publish_job, "actions/attest-build-provenance@").empty?, "#{publish_job} must not attest a candidate tag")
  assert(action_steps(publish_job, "actions/attest@").empty?, "#{publish_job} must not attest a candidate tag")

  promote = JOBS.fetch(promote_job)
  assert(Array(promote.fetch("needs")).include?(publish_job), "#{promote_job} must consume #{publish_job}")
  assert((%w[publish-backend publish-frontend] - Array(promote.fetch("needs"))).empty?, "#{promote_job} must wait for both candidate images")
  promote_runs = run_text(promote_job)
  assert(promote_runs.include?("commits/main") && promote_runs.include?("GITHUB_SHA"), "#{promote_job} must inspect current main")
  assert(promote_runs.include?("imagetools create") && promote_runs.include?("digest"), "#{promote_job} must promote the published digest without rebuilding")
  assert(promote_runs.include?("GITHUB_RUN_ID") && promote_runs.include?("GITHUB_RUN_ATTEMPT"), "#{promote_job} must promote the run-specific candidate")
  assert(promote_runs.include?("${GITHUB_SHA}-arm64") && promote_runs.include?("immutable_tag=\"$GITHUB_SHA\""), "#{promote_job} must create architecture-specific immutable tags")
  assert(promote_runs.include?("latest") && promote_runs.include?("latest-arm64"), "#{promote_job} must preserve latest for amd64 and suffix ARM latest")
  immutable_position = promote_runs.index('--tag "${IMAGE_NAME}:${immutable_tag}"')
  main_lookup_position = promote_runs.index("commits/main")
  floating_position = promote_runs.index('--tag "${IMAGE_NAME}:${floating_tag}"')
  assert(immutable_position && main_lookup_position && floating_position && immutable_position < main_lookup_position && main_lookup_position < floating_position, "#{promote_job} must create the immutable tag before gating the floating tag")
  assert(promote_runs.include?('if current_sha=$('), "#{promote_job} must handle current-main API failure without losing the immutable tag")
  assert(promote_runs.include?('if [ "$current_sha" = "$GITHUB_SHA" ]'), "#{promote_job} must update the floating tag only for current main")
  assert(!promote_runs.include?("exit 1"), "#{promote_job} stale and API-failure branches must degrade to the immutable tag")
  attestations = action_steps(promote_job, "actions/attest@")
  assert(attestations.length == 1, "#{promote_job} must attest the final digest once")
  promotion_step_index = steps(promote_job).index { |step| step["id"] == "promote" }
  attestation_step_index = steps(promote_job).index(attestations.first)
  assert(promotion_step_index && attestation_step_index > promotion_step_index, "#{promote_job} must attest only after final tag promotion")
  assert(attestations.first.fetch("uses").end_with?("@1e69f48acb82d1966a394da916b4c1698aa569d6"), "#{promote_job} must pin actions/attest v4.2.2")
  attestation_options = attestations.first.fetch("with")
  assert(attestation_options.key?("subject-digest") && attestation_options.fetch("push-to-registry") == true, "#{promote_job} must push digest-bound provenance")
  assert(attestation_options.fetch("create-storage-record") == false, "#{promote_job} must not create a duplicate attestation storage record")
end

REGISTRY_JOBS.each do |job_name|
  job = JOBS.fetch(job_name)
  permissions = job.fetch("permissions")
  assert(permissions.fetch("packages") == "write", "#{job_name} must have packages: write")
  assert(action_steps(job_name, "actions/checkout@").empty?, "#{job_name} must not check out source")
  commands = run_text(job_name)
  assert(!commands.match?(/(^|\s)(go|npm|make)\s/), "#{job_name} must not execute source tooling")
  assert(!commands.include?("scripts/"), "#{job_name} must not execute repository scripts")
  assert(!commands.match?(/docker\s+(build\s|compose\s)/), "#{job_name} must not build from source")
  steps(job_name).select { |step| step["run"] }.each do |step|
    assert(step.fetch("run").start_with?("set -euo pipefail\n"), "#{job_name} shell steps must fail closed")
  end
  logout_steps = steps(job_name).select { |step| step.fetch("run", "").include?("docker logout ghcr.io") }
  assert(logout_steps.length == 1 && logout_steps.first.fetch("if") == "always()", "#{job_name} must always clear GHCR credentials")
end

JOBS.each do |job_name, job|
  Array(job["steps"]).each_with_index do |step, index|
    next unless step["run"]

    Tempfile.create(["hookfly-workflow-", ".sh"]) do |file|
      file.write("set -e\n#{step.fetch("run")}\n")
      file.flush
      _output, status = Open3.capture2e("bash", "-n", file.path)
      assert(status.success?, "invalid shell syntax in #{job_name} step #{index + 1}")
    end
  end
end

compose_services = COMPOSE.fetch("services")
backend = compose_services.fetch("backend")
frontend = compose_services.fetch("frontend")
assert(Array(backend["ports"]).empty?, "backend listeners must not be published directly on the host")
assert(Array(frontend["ports"]).empty?, "frontend must be exposed only through Traefik")

backend_labels = backend.fetch("labels")
frontend_labels = frontend.fetch("labels")
hooks_rule = backend_labels.fetch("traefik.http.routers.hookfly-hooks.rule", "")
frontend_rule = frontend_labels.fetch("traefik.http.routers.hookfly.rule", "")
assert(hooks_rule.include?("Host(") && hooks_rule.include?("PathPrefix(`/hooks/`)"), "backend Traefik router must own only the host webhook path")
assert(frontend_rule.include?("Host(") && !frontend_rule.include?("Path"), "frontend Traefik router must own the host-wide route")
assert(backend_labels.fetch("traefik.http.routers.hookfly-hooks.priority", "0").to_i > frontend_labels.fetch("traefik.http.routers.hookfly.priority", "0").to_i, "webhook router must have higher priority than the frontend router")
assert(backend_labels["traefik.http.services.hookfly-hooks.loadbalancer.server.port"] == "8080", "Traefik webhook service must use the backend public listener")
assert(frontend_labels["traefik.http.services.hookfly.loadbalancer.server.port"] == "8080", "Traefik frontend service must use Nginx port 8080")
assert(!backend_labels.values.include?("8081"), "Traefik must not route directly to the backend management listener")

backend_networks = backend.fetch("networks").keys
frontend_networks = frontend.fetch("networks").keys
assert((%w[private proxy] - backend_networks).empty?, "backend must join private and Traefik networks")
assert((%w[private proxy] - frontend_networks).empty?, "frontend must join private and Traefik networks")
assert(COMPOSE.fetch("networks").fetch("private").fetch("internal") == true, "backend management traffic must use an internal Compose network")
assert(COMPOSE.fetch("networks").fetch("proxy").fetch("external") == true, "Traefik network must remain external")

puts "Container workflow checks passed."
