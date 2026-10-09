import { pluginsApi } from '@/lib/endpoints'

/** Fetches a version ZIP (session cookie + error handling) and saves it via a Blob link. */
export async function downloadVersionZip(pluginId: string, versionId: string, fallbackName: string): Promise<void> {
  const { blob, filename } = await pluginsApi.exportZip(pluginId, versionId)
  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = filename || fallbackName
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  }
  finally {
    // Give the browser a moment to start the download before revoking.
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
  }
}
