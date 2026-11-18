import { injectDialogRootContext } from 'reka-ui'
import { ref, watch } from 'vue'

export function useDialogPortalKey(forceMount: () => boolean | undefined) {
  const { open, unmountOnHide } = injectDialogRootContext()
  const key = ref(0)

  // Closed portals already have Teleport anchors in DOM order. Remount the
  // portal on open so its overlay and panel stack above previously opened ones.
  // Keep persistent contents mounted and leave exit animations to Reka.
  watch(open, (isOpen) => {
    if (isOpen && unmountOnHide.value && !forceMount()) key.value++
  })

  return key
}
