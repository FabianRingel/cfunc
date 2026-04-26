import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import type { MetricPoint } from '../useMetricsHistory'
import type { FunctionStats, State } from '../types'

const ACCENT = '#4cc9f0'
const GOOD = '#51cf66'
const BAD = '#ff6b6b'
const MUTED = '#8b95a4'
const GRID = '#242c3d'
const PANEL = '#131722'

interface Props {
  state: State | null
  history: MetricPoint[]
}

export function Overview({ state, history }: Props) {
  const last = history[history.length - 1]
  const totalInvokes = last?.totalInvokes ?? 0
  const totalErrors = last?.totalErrors ?? 0
  const errorRate =
    totalInvokes > 0 ? (totalErrors / totalInvokes) * 100 : 0
  const warm = last?.warm ?? 0
  const fns = state?.functions.length ?? 0
  const rps = last?.rps ?? 0
  const eps = last?.eps ?? 0

  const top = topInvokes(state, 6)

  return (
    <section className="grid gap-4 grid-cols-2 lg:grid-cols-4">
      <KPI label="req / s"     value={rps.toFixed(1)}  color={ACCENT} />
      <KPI label="err / s"     value={eps.toFixed(2)}  color={eps > 0 ? BAD : MUTED} />
      <KPI label="warm fns"    value={`${warm} / ${fns}`} color={GOOD} />
      <KPI label="error rate"  value={`${errorRate.toFixed(2)}%`} color={errorRate > 1 ? BAD : MUTED} />

      <ChartCard title="Requests / sec (last 2 min)" className="col-span-2">
        <ResponsiveContainer width="100%" height={180}>
          <AreaChart data={history} margin={{ top: 6, right: 6, left: 0, bottom: 0 }}>
            <defs>
              <linearGradient id="g-rps" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%"  stopColor={ACCENT} stopOpacity={0.5} />
                <stop offset="100%" stopColor={ACCENT} stopOpacity={0} />
              </linearGradient>
            </defs>
            <CartesianGrid stroke={GRID} strokeDasharray="3 3" />
            <XAxis dataKey="ts" tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} minTickGap={32} />
            <YAxis tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} width={32} />
            <Tooltip {...tooltipStyle} />
            <Area type="monotone" dataKey="rps" stroke={ACCENT} fill="url(#g-rps)" isAnimationActive={false} />
          </AreaChart>
        </ResponsiveContainer>
      </ChartCard>

      <ChartCard title="Errors / sec" className="col-span-2">
        <ResponsiveContainer width="100%" height={180}>
          <AreaChart data={history} margin={{ top: 6, right: 6, left: 0, bottom: 0 }}>
            <defs>
              <linearGradient id="g-eps" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%"  stopColor={BAD} stopOpacity={0.5} />
                <stop offset="100%" stopColor={BAD} stopOpacity={0} />
              </linearGradient>
            </defs>
            <CartesianGrid stroke={GRID} strokeDasharray="3 3" />
            <XAxis dataKey="ts" tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} minTickGap={32} />
            <YAxis tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} width={32} allowDecimals={false} />
            <Tooltip {...tooltipStyle} />
            <Area type="monotone" dataKey="eps" stroke={BAD} fill="url(#g-eps)" isAnimationActive={false} />
          </AreaChart>
        </ResponsiveContainer>
      </ChartCard>

      <ChartCard title="Warm function count" className="col-span-2">
        <ResponsiveContainer width="100%" height={180}>
          <LineChart data={history} margin={{ top: 6, right: 6, left: 0, bottom: 0 }}>
            <CartesianGrid stroke={GRID} strokeDasharray="3 3" />
            <XAxis dataKey="ts" tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} minTickGap={32} />
            <YAxis tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} width={32} allowDecimals={false} />
            <Tooltip {...tooltipStyle} />
            <Line
              type="stepAfter"
              dataKey="warm"
              stroke={GOOD}
              strokeWidth={2}
              dot={false}
              isAnimationActive={false}
            />
          </LineChart>
        </ResponsiveContainer>
      </ChartCard>

      <ChartCard title="Avg latency across warm fns (ms)" className="col-span-2">
        <ResponsiveContainer width="100%" height={180}>
          <LineChart data={history} margin={{ top: 6, right: 6, left: 0, bottom: 0 }}>
            <CartesianGrid stroke={GRID} strokeDasharray="3 3" />
            <XAxis dataKey="ts" tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} minTickGap={32} />
            <YAxis tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} width={36} />
            <Tooltip {...tooltipStyle} />
            <Line
              type="monotone"
              dataKey="avgMs"
              stroke={ACCENT}
              strokeWidth={2}
              dot={false}
              isAnimationActive={false}
            />
          </LineChart>
        </ResponsiveContainer>
      </ChartCard>

      <ChartCard title="Top functions (invokes total)" className="col-span-2 lg:col-span-4">
        <ResponsiveContainer width="100%" height={Math.max(140, top.length * 28)}>
          <BarChart data={top} layout="vertical" margin={{ top: 6, right: 24, left: 12, bottom: 0 }}>
            <CartesianGrid stroke={GRID} strokeDasharray="3 3" horizontal={false} />
            <XAxis type="number" tick={{ fill: MUTED, fontSize: 10 }} stroke={GRID} />
            <YAxis
              type="category"
              dataKey="name"
              tick={{ fill: MUTED, fontSize: 11 }}
              stroke={GRID}
              width={140}
            />
            <Tooltip {...tooltipStyle} />
            <Bar dataKey="invokes" isAnimationActive={false} radius={[0, 3, 3, 0]}>
              {top.map((entry, i) => (
                <Cell key={i} fill={entry.errors > 0 ? BAD : ACCENT} />
              ))}
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      </ChartCard>
    </section>
  )
}

function topInvokes(state: State | null, n: number): FunctionStats[] {
  if (!state) return []
  return [...state.functions]
    .filter((f) => f.invokes > 0)
    .sort((a, b) => b.invokes - a.invokes)
    .slice(0, n)
}

function KPI({
  label,
  value,
  color,
}: {
  label: string
  value: string
  color: string
}) {
  return (
    <div className="bg-[var(--color-panel)] border border-[var(--color-border)] rounded-lg p-4">
      <div className="text-[10px] uppercase tracking-wider text-[var(--color-muted)] font-semibold">
        {label}
      </div>
      <div
        className="text-2xl font-mono font-semibold mt-1 tabular-nums"
        style={{ color }}
      >
        {value}
      </div>
    </div>
  )
}

function ChartCard({
  title,
  children,
  className = '',
}: {
  title: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={`bg-[var(--color-panel)] border border-[var(--color-border)] rounded-lg p-3 ${className}`}
    >
      <div className="text-[10px] uppercase tracking-wider text-[var(--color-muted)] font-semibold mb-2 px-1">
        {title}
      </div>
      {children}
    </div>
  )
}

const tooltipStyle = {
  contentStyle: {
    background: PANEL,
    border: `1px solid ${GRID}`,
    borderRadius: 4,
    fontSize: 11,
    fontFamily: 'ui-monospace, monospace',
    color: '#d6dde7',
  },
  cursor: { stroke: GRID, strokeWidth: 1 },
  labelStyle: { color: MUTED, fontSize: 10 },
} as const
