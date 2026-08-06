// The control skins the wizard steps and the first-run hero share. One
// definition keeps a rounding or focus change from forking per step.

export const inputClasses = `rounded-[10px] border border-line bg-panel-2 px-3 py-2 font-mono
  text-sm outline-none transition-colors focus:border-brand`;

export const secondaryButton = `rounded-full border border-line px-5 py-2 text-sm font-medium
  text-ink transition-colors hover:border-brand`;

export const primaryButton = `rounded-full bg-brand-dim px-6 py-2 text-sm font-semibold text-white
  shadow-[0_1px_2px_rgb(0_0_0_/_0.25)] transition-[filter] hover:brightness-110
  disabled:opacity-50 disabled:hover:brightness-100`;
