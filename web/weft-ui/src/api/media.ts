// Media references to external storage. See docs/weft/API.md "Files and media".
import { post } from './client'

export const mediaApi = {
  /** Download URLs (presigned, or this server's /file/content) for file digests, weft-file://, s3:// or http(s) uris. */
  resolveUrls: (project_id: string, uris: string[]) =>
    post<{ download_urls: string[]; expires_at: string | null }>('/files/resolve_url', { project_id, uris }),
}
