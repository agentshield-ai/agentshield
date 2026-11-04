// Media references to external storage. See docs/weft/API.md "Files and media".
import { post } from './client'

/** Why one URI could not be resolved (`per_uri_errors`). */
export interface ResolveUriError {
  code: 'bucket_not_registered' | 'no_files_backend' | 'not_project_file' | 'file_not_found' | 'invalid_uri' | 'bucket_error' | (string & {})
  message: string
}

/**
 * /api/v1/files/resolve response. With `per_uri_errors` an unresolvable URI does
 * not fail the request: its `download_urls` entry is "" and `errors` (aligned
 * with `uris`) says why.
 */
export interface ResolveUrlRes {
  download_urls: string[]
  expires_at: string | null
  errors?: (ResolveUriError | null)[]
}

export const mediaApi = {
  /** Download URLs (presigned, or this server's /api/v1/files/{digest}) for file digests, weft-file://, s3:// or http(s) uris, with per-URI errors. */
  resolveUrls: (project_id: string, uris: string[]) => post<ResolveUrlRes>('/api/v1/files/resolve', { project_id, uris, per_uri_errors: true }),
}
