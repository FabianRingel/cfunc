import { useState } from 'react'
import type { LayerSummary } from '../types'

interface Props {
  layers: LayerSummary[]
}

// LayersPanel surfaces the page-cache sharing story. Each row is one
// host directory; "shared by" counts how many functions reference it,
// "warm" shows how many are currently mounted. Saved-instances = ref-1
// because the first instance pays full cost; every additional instance
// reuses the same inode and contributes nothing further to RAM.
export function LayersPanel({ layers }: Props) {
  const [open, setOpen] = useState<string | null>(null)

  if (layers.length === 0) {
    return (
      <div className="text-[var(--color-muted)] italic text-sm">
        No layers configured.
      </div>
    )
  }

  const totalRefs   = layers.reduce((s, l) => s + l.ref_count, 0)
  const totalUnique = layers.length
  const savedRefs   = layers.reduce((s, l) => s + Math.max(0, l.ref_count - 1), 0)
  const sharingPct  = totalRefs > 0 ? (savedRefs / totalRefs) * 100 : 0

  return (
    <div className="flex flex-col">
      <div className="flex flex-wrap gap-x-6 gap-y-1 mb-3 text-[11px] font-mono text-[var(--color-muted)]">
        <Stat label="unique"  value={String(totalUnique)} />
        <Stat label="refs"    value={String(totalRefs)} />
        <Stat label="dedup'd" value={`${savedRefs} (${sharingPct.toFixed(0)}%)`} />
        <span className="ml-auto text-[10px] uppercase tracking-wider">
          Same host_path = same inode = single page-cache copy
        </span>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-[12.5px] font-mono border-collapse">
          <thead>
            <tr className="text-left text-[10px] uppercase tracking-wider text-[var(--color-muted)] font-medium">
              <Th>Layer</Th>
              <Th>Mount</Th>
              <Th align="right">Refs</Th>
              <Th align="right">Warm</Th>
              <Th>Sharing</Th>
              <Th>References</Th>
            </tr>
          </thead>
          <tbody>
            {layers.map((l) => {
              const id = l.host_path
              const isOpen = open === id
              return (
                <Row
                  key={id}
                  layer={l}
                  open={isOpen}
                  onToggle={() => setOpen(isOpen ? null : id)}
                />
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function Row({
  layer,
  open,
  onToggle,
}: {
  layer: LayerSummary
  open: boolean
  onToggle: () => void
}) {
  const sharing = layer.ref_count > 1 ? 'shared' : 'single'
  const refsPreview = layer.references.slice(0, 4).join(', ') +
    (layer.references.length > 4 ? `, +${layer.references.length - 4}` : '')

  return (
    <>
      <tr
        onClick={onToggle}
        className="cursor-pointer border-t border-[var(--color-border)] hover:bg-[var(--color-panel-2)] transition-colors"
      >
        <Td>{layer.name}</Td>
        <Td className="text-[var(--color-accent)]">{layer.mount_path}</Td>
        <Td align="right">{layer.ref_count}</Td>
        <Td align="right">{layer.warm_refs}</Td>
        <Td>
          <SharingBadge kind={sharing} count={layer.ref_count} />
        </Td>
        <Td className="text-[var(--color-muted)] truncate max-w-md">
          {refsPreview}
        </Td>
      </tr>
      {open && (
        <tr className="bg-[var(--color-panel-2)] border-t border-[var(--color-border)]">
          <td colSpan={6} className="px-3 py-3 text-xs">
            <div className="grid grid-cols-2 gap-x-8 gap-y-1.5 max-w-3xl">
              <KV k="Host path"  v={layer.host_path} />
              <KV k="Mount path" v={layer.mount_path} />
              <KV k="Refs"       v={`${layer.ref_count} functions`} />
              <KV k="Currently warm" v={`${layer.warm_refs}`} />
            </div>
            <div className="mt-3">
              <div className="text-[10px] uppercase tracking-wider text-[var(--color-muted)] mb-1">
                Referenced by
              </div>
              <div className="flex flex-wrap gap-1.5">
                {layer.references.map((r) => (
                  <span
                    key={r}
                    className="text-[11px] px-2 py-0.5 rounded bg-[var(--color-panel)] border border-[var(--color-border)]"
                  >
                    {r}
                  </span>
                ))}
              </div>
            </div>
          </td>
        </tr>
      )}
    </>
  )
}

function SharingBadge({ kind, count }: { kind: 'shared' | 'single'; count: number }) {
  if (kind === 'shared') {
    return (
      <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-[rgba(76,201,240,0.12)] text-[var(--color-accent)]">
        ×{count} shared
      </span>
    )
  }
  return (
    <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-[var(--color-panel-2)] text-[var(--color-muted)] border border-[var(--color-border)]">
      single
    </span>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <span>
      <span className="text-[10px] uppercase tracking-wider opacity-70 mr-1">{label}</span>
      <span className="text-[var(--color-text)]">{value}</span>
    </span>
  )
}

function Th({
  children,
  align = 'left',
}: { children: React.ReactNode; align?: 'left' | 'right' }) {
  return (
    <th className={`px-3 py-2 ${align === 'right' ? 'text-right' : 'text-left'} font-medium`}>
      {children}
    </th>
  )
}

function Td({
  children,
  className = '',
  align = 'left',
}: {
  children: React.ReactNode
  className?: string
  align?: 'left' | 'right'
}) {
  return (
    <td
      className={`px-3 py-2 whitespace-nowrap ${
        align === 'right' ? 'text-right tabular-nums' : ''
      } ${className}`}
    >
      {children}
    </td>
  )
}

function KV({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex gap-3">
      <span className="text-[var(--color-muted)] w-28 shrink-0">{k}</span>
      <span className="text-[var(--color-text)] truncate font-mono text-[11px]">{v}</span>
    </div>
  )
}
