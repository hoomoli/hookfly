import type { Repository } from "./types";

type RepositoryIdentity = Pick<Repository, "provider" | "source_id"> & { repository: string };

export function repositoryDisplayName(repositories: Repository[], identity: RepositoryIdentity): string {
  if (!identity.repository) return "";
  return repositories.find((candidate) =>
    candidate.provider === identity.provider
    && candidate.source_id === identity.source_id
    && candidate.id === identity.repository
  )?.name || identity.repository;
}
