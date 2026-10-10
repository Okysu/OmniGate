<script setup lang="ts">
import type { Component } from 'vue'
import type { ClientKind, LogClient } from '@/lib/types'
import { computed } from 'vue'
import { Bot, CircleHelp, Globe, MessageSquare, Package } from '@lucide/vue'
import { clientKind, clientLabel, clientTitle } from '@/lib/clients'
import { useClientsStore } from '@/stores/clients'

/**
 * Detected client of a request (phase13-api.md): kind icon + name, version in the tooltip
 * (and inline with `showVersion`). Icons are generic lucide icons per kind, no brand assets.
 */
const props = withDefaults(defineProps<{
  client: LogClient | null | undefined
  showVersion?: boolean
}>(), { showVersion: false })

const clients = useClientsStore()

const KIND_ICONS: Record<ClientKind, Component> = { agent: Bot, chat: MessageSquare, sdk: Package, tool: Globe, unknown: CircleHelp }

const c = computed<LogClient>(() => props.client ?? { id: 'unknown', name: '未知', version: null })
const kind = computed(() => clientKind(clients.list, c.value.id))
const title = computed(() => clientTitle(c.value, clients.items ? kind.value : undefined))
</script>

<template>
  <span class="inline-flex max-w-full min-w-0 items-center gap-1" :class="c.id === 'unknown' ? 'text-muted-foreground' : ''" :title="title" data-testid="client-badge">
    <component :is="KIND_ICONS[kind]" class="size-3.5 shrink-0" aria-hidden="true" />
    <span class="truncate">{{ showVersion ? clientLabel(c) : c.name }}</span>
  </span>
</template>
