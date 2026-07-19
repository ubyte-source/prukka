/** apiBase resolves the explicit mode marker injected by the serving surface. */
export function apiBase(configured: string | null): string {
  if (configured === "same-origin") return "";
  if (!configured) throw new Error("missing prukka-api-base metadata");

  const parsed = new URL(configured);
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
    parsed.username ||
    parsed.password ||
    parsed.pathname !== "/" ||
    parsed.search ||
    parsed.hash
  ) {
    throw new Error("invalid prukka-api-base metadata");
  }

  return parsed.origin;
}
