import { computed, onMounted } from 'vue'
import { DOCS_URL } from '@/config/nav'
import { announcementOf, docsUrlOf, landingEnabledOf, siteNameOf } from '@/lib/site'
import { useSystemStore } from '@/stores/system'

/** Site name, announcement, landing switch and docs link from `/api/system/info`. */
export function useSite() {
  const system = useSystemStore()
  onMounted(() => {
    void system.ensureLoaded()
  })
  const siteName = computed(() => siteNameOf(system.info))
  const announcement = computed(() => announcementOf(system.info))
  const landingEnabled = computed(() => landingEnabledOf(system.info))
  const docsUrl = computed(() => docsUrlOf(system.info, DOCS_URL))
  return { siteName, announcement, landingEnabled, docsUrl }
}
