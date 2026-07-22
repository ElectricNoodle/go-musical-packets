export type FlowState = 'ignore' | 'monitor' | 'play'
export type MusicalMode = 'ionian' | 'dorian' | 'phrygian' | 'lydian' | 'mixolydian' | 'aeolian' | 'locrian'
export type RuntimeRole = 'standalone' | 'edge' | 'host'
export type PacketProtocol = 'tcp' | 'udp' | 'icmp' | 'icmp6' | 'other'

export interface Status {
  state: string
  revision: string
  pending_revision?: string
  writable: boolean
  warning?: string
}

export interface CaptureInterface {
  name: string
  description: string
  addresses: string[]
  up: boolean
  loopback: boolean
}

export interface InterfacesDocument {
  configured: string
  selected: string
  interfaces: CaptureInterface[]
}

export interface MIDIDevice {
  number: number
  name: string
}

export interface MIDIDevicesDocument {
  enabled: boolean
  discovery: 'disabled' | 'ok' | 'error'
  connected: boolean
  current: MIDIDevice | null
  devices: MIDIDevice[]
}

export interface Configuration {
  instance: {
    id: string
    role: RuntimeRole
  }
  capture: {
    enabled: boolean
    interface: string
    bpf: string
    snapshot_length: number
    promiscuous: boolean
  }
  mapping: {
    version: string
    seed: string
    default_state: FlowState
    default_channel: number
    minimum_note: number
    maximum_note: number
    minimum_duration: string
    maximum_duration: string
  }
  performance: {
    packet_queue_capacity: number
    note_queue_capacity: number
    ui_queue_capacity: number
    flow_registry_capacity: number
    flow_ttl: string
    maximum_notes_per_second: number
    maximum_polyphony: number
    minimum_retrigger_interval: string
  }
  midi: {
    enabled: boolean
    exact_device_name: string
    device_name_regexp: string
    poll_interval: string
  }
  server: {
    listen_address: string
    read_timeout: string
    write_timeout: string
  }
  peer: {
    enabled: boolean
    url: string
    token: string
    queue_capacity: number
    maximum_connections: number
    recent_ttl: string
    reconnect_base: string
    reconnect_limit: string
    stale_after: string
  }
  metrics: {
    namespace: string
  }
  logging: {
    level: 'debug' | 'info' | 'warn' | 'error'
    format: 'text' | 'json'
  }
  rules: RuleConfig[]
}

export interface ConfigDocument {
  config: Configuration
  revision: string
}

export interface Validation {
  revision: string
  hot_fields: string[]
  restart_required_fields: string[]
}

export interface RuntimeSnapshot {
  status: Status
  config: ConfigDocument
  pending?: ConfigDocument
  interfaces: InterfacesDocument
  midi: MIDIDevicesDocument
}

export interface LiveNoteEvent {
	id: string
	origin: string
	sequence: number
	mapping_version: string
	flow_id: string
	mode: string
	root: number
	note: number
	velocity: number
	duration_ms: number
	channel: number
	created_at: string
	accepted_at: string
}

export interface LiveNoteBatch {
	type: 'notes'
	sent_at: string
	dropped: number
	packet_total: number
	note_total: number
	notes: LiveNoteEvent[]
}

export interface FlowEndpoint {
  address: string
  port: number
}

export interface FlowSnapshot {
  id: string
  protocol: PacketProtocol
  endpoint_a: FlowEndpoint
  endpoint_b: FlowEndpoint
  latest_source: FlowEndpoint
  latest_destination: FlowEndpoint
  first_seen: string
  last_seen: string
  packets: number
  bytes: number
  packets_a_to_b: number
  packets_b_to_a: number
  muted: boolean
  soloed: boolean
  state: FlowState
  channel: number
  rule_id?: string
  rule_tier: string
  rule_name?: string
  decision_reason: string
  matched_predicates: string[]
  mode: string
  root: number
  fixed_identity: boolean
}

export interface FlowOverlay {
  muted: string[]
  soloed: string[]
}

export interface FlowPage {
  flows: FlowSnapshot[]
  overlay: FlowOverlay
  total: number
  limit: number
  truncated: boolean
}

export interface RulePortRange {
  minimum: number
  maximum: number
}

export interface RuleSizeRange {
  minimum: number
  maximum: number
}

export interface RuleMatch {
  exact_flow_id?: string
  source_cidr?: string
  destination_cidr?: string
  protocol?: PacketProtocol
  source_ports?: RulePortRange
  destination_ports?: RulePortRange
  wire_size?: RuleSizeRange
  required_tcp_flags?: Array<'fin' | 'syn' | 'rst' | 'psh' | 'ack' | 'urg'>
}

export interface RuleConfig {
  id: string
  name: string
  enabled: boolean
  match: RuleMatch
  action: {
    state: FlowState
    channel: number
    mode?: MusicalMode
    root?: number
  }
}

export interface RulesDocument {
  revision: string
  etag: string
  writable: boolean
  rules: RuleConfig[]
}

export interface PeerOutbound {
  enabled: boolean
  target: string
  remote_instance?: string
  state: 'disabled' | 'disconnected' | 'connecting' | 'connected' | 'backoff'
  protocol_version?: string
  mapping_version?: string
  queue: { depth: number, capacity: number }
  sent_total: number
  dropped_full: number
  dropped_stale: number
  reconnects: number
  send_rate: number
  last_sent_at?: string
  connected_at?: string
  last_attempt_at?: string
  next_retry_at?: string
  rtt_ms: number
  last_error?: string
  active_channels: number[]
}

export interface ConnectedNode {
  instance_id: string
  remote_address: string
  state: 'connected' | 'disconnected'
  authenticated: boolean
  protocol_version: string
  mapping_version: string
  connected_at: string
  disconnected_at?: string
  last_seen_at: string
  note_rate: number
  received_total: number
  accepted_total: number
  rejected_total: number
  duplicate_total: number
  stale_total: number
  active_channels: number[]
}

export interface PeersDocument {
  role: RuntimeRole
  outbound?: PeerOutbound
  nodes: ConnectedNode[]
}

export interface ProblemDocument {
  status: number
  code: string
  detail: string
  fields?: string[]
}
