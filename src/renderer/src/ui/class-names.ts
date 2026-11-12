/** Bind feature-owned classes while preserving explicit shared UI surface classes. */
export function bindStyles(
  styles: Readonly<Record<string, string>>
): (value: string | undefined) => string {
  return (value) =>
    (value ?? '')
      .split(/\s+/)
      .map((name) => styles[name] ?? name)
      .join(' ')
}
