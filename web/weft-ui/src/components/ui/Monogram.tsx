// Monogram: the round initial that stands for who acted (DESIGN.md §7.1
// rule 9, §7.2 rule 1). Agents are told apart by name and monogram, never by
// colour, so the same name always gives the same letter. The name is shown
// next to it, so the monogram is decorative (aria-hidden).
//
//   <Monogram name="coder" />                 // dark disc, "C"
//   <Monogram name="User" variant="user" />   // grey disc, "U"
//   <Monogram name="reviewer" variant="nested" />  // white disc with a ring (inside a hand-off)

export type MonogramVariant = 'agent' | 'user' | 'nested'

/** The letter a name is shown as: its first character, upper-cased (any script). */
export function monogramLetter(name: string | null | undefined): string {
  const first = Array.from((name ?? '').trim())[0]
  return first ? first.toLocaleUpperCase() : '?'
}

export function Monogram({ name, variant = 'agent', size = 30 }: { name: string | null | undefined; variant?: MonogramVariant; size?: number }) {
  return (
    <span className={`monogram mg-${variant}`} aria-hidden="true" style={{ width: size, height: size, fontSize: Math.round(size * 0.43) }}>
      {monogramLetter(name)}
    </span>
  )
}
