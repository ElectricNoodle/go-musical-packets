import { vi } from 'vitest'
import type { Configuration, RuntimeSnapshot } from '../api/types'
import type { ManagementClient } from '../api/client'

export const configuration: Configuration = {
  instance: { id: 'studio-node', role: 'standalone' },
  capture: { enabled: true, interface: 'auto', bpf: 'ip or ip6', snapshot_length: 65535, promiscuous: true },
  mapping: {
    version: 'flow-mode-v1', seed: '<write-only>', default_state: 'monitor', default_channel: 1,
    minimum_note: 36, maximum_note: 96, minimum_duration: '50ms', maximum_duration: '2s',
  },
  performance: {
    packet_queue_capacity: 4096, note_queue_capacity: 1024, ui_queue_capacity: 512,
    flow_registry_capacity: 10000, flow_ttl: '5m', maximum_notes_per_second: 100,
    maximum_polyphony: 32, minimum_retrigger_interval: '10ms',
  },
  midi: { enabled: true, exact_device_name: '', device_name_regexp: '', poll_interval: '2s' },
  server: { listen_address: '127.0.0.1:8080', read_timeout: '10s', write_timeout: '10s' },
  peer: { enabled: false, url: '<write-only-url>', reconnect_base: '500ms', reconnect_limit: '30s', stale_after: '500ms' },
  metrics: { namespace: 'musical_packets' },
  logging: { level: 'info', format: 'text' },
  rules: [],
}

export const snapshot: RuntimeSnapshot = {
  status: { state: 'ready', revision: 'public-revision', writable: true },
  config: { config: configuration, revision: '"public-revision"' },
  interfaces: {
    configured: 'auto', selected: 'en0', interfaces: [
      { name: 'en0', description: 'Ethernet', addresses: ['192.0.2.10/24'], up: true, loopback: false },
      { name: 'lo0', description: 'Loopback', addresses: ['127.0.0.1/8'], up: true, loopback: true },
    ],
  },
  midi: {
    enabled: true, discovery: 'ok', connected: true,
    current: { number: 2, name: 'USB Synth' }, devices: [{ number: 2, name: 'USB Synth' }],
  },
}

export function stubClient(overrides: Partial<ManagementClient> = {}): ManagementClient {
  return {
    getStatus: vi.fn().mockResolvedValue(snapshot.status),
    getConfig: vi.fn().mockResolvedValue(snapshot.config),
    getInterfaces: vi.fn().mockResolvedValue(snapshot.interfaces),
    getMIDI: vi.fn().mockResolvedValue(snapshot.midi),
    validateConfig: vi.fn().mockResolvedValue({ revision: 'public-revision', hot_fields: [], restart_required_fields: [] }),
    updateConfig: vi.fn().mockResolvedValue(snapshot.config),
    auditionMIDI: vi.fn().mockResolvedValue(undefined),
    panicMIDI: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  }
}
