import { describe, expect, it } from "vitest";
import { repositoryDisplayName } from "./repositories";
import type { Repository } from "./types";

const repositories: Repository[] = [
  { provider: "gitlab", source_id: "gitlab-a", id: "app", name: "GitLab Application" },
  { provider: "github", source_id: "github-a", id: "app", name: "GitHub Application" },
];

describe("repositoryDisplayName", () => {
  it("matches a configured name by provider, source, and repository ID", () => {
    expect(repositoryDisplayName(repositories, { provider: "gitlab", source_id: "gitlab-a", repository: "app" })).toBe("GitLab Application");
    expect(repositoryDisplayName(repositories, { provider: "github", source_id: "github-a", repository: "app" })).toBe("GitHub Application");
  });

  it("falls back to the stored repository ID when it is no longer configured", () => {
    expect(repositoryDisplayName(repositories, { provider: "gitlab", source_id: "gitlab-a", repository: "removed" })).toBe("removed");
  });

  it("keeps an empty repository empty", () => {
    expect(repositoryDisplayName(repositories, { provider: "gitlab", source_id: "gitlab-a", repository: "" })).toBe("");
  });
});
