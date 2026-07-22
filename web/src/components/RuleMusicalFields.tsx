import type { FlowState, MusicalMode, RuleConfig } from '../api/types'

export const rootNames = ['C', 'C♯', 'D', 'E♭', 'E', 'F', 'F♯', 'G', 'A♭', 'A', 'B♭', 'B']
export const musicalModes: MusicalMode[] = ['ionian', 'dorian', 'phrygian', 'lydian', 'mixolydian', 'aeolian', 'locrian']

type RuleAction = RuleConfig['action']

export function withActionState(action: RuleAction, state: FlowState): RuleAction {
  if (state === 'play') return { ...action, state }
  return { state, channel: action.channel }
}

export function MusicalIdentityFields({
  action,
  fallbackMode = 'ionian',
  fallbackRoot = 0,
  onChange,
}: {
  action: RuleAction
  fallbackMode?: string
  fallbackRoot?: number
  onChange: (action: RuleAction) => void
}) {
  const fixed = action.mode !== undefined && action.root !== undefined
  const safeMode = musicalModes.includes(fallbackMode as MusicalMode) ? fallbackMode as MusicalMode : 'ionian'
  const safeRoot = Number.isInteger(fallbackRoot) && fallbackRoot >= 0 && fallbackRoot <= 11 ? fallbackRoot : 0
  return (
    <>
      <label className="field">
        <span>Musical identity</span>
        <select
          value={fixed ? 'fixed' : 'automatic'}
          disabled={action.state !== 'play'}
          onChange={(event) => onChange(event.target.value === 'fixed'
            ? { ...action, mode: action.mode ?? safeMode, root: action.root ?? safeRoot }
            : { state: action.state, channel: action.channel })}
        >
          <option value="automatic">Automatic per flow</option>
          <option value="fixed">Fixed across matches</option>
        </select>
        <small>{fixed ? 'Every flow matched by this rule uses one key and mode.' : 'Each matched flow keeps its deterministic identity.'}</small>
      </label>
      <label className="field">
        <span>Root key</span>
        <select disabled={!fixed || action.state !== 'play'} value={action.root ?? safeRoot} onChange={(event) => onChange({ ...action, root: Number(event.target.value) })}>
          {rootNames.map((name, root) => <option key={name} value={root}>{name}</option>)}
        </select>
      </label>
      <label className="field">
        <span>Mode</span>
        <select disabled={!fixed || action.state !== 'play'} value={action.mode ?? safeMode} onChange={(event) => onChange({ ...action, mode: event.target.value as MusicalMode })}>
          {musicalModes.map((mode) => <option key={mode} value={mode}>{mode[0]?.toUpperCase()}{mode.slice(1)}</option>)}
        </select>
      </label>
    </>
  )
}
