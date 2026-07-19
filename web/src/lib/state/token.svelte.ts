// Control-token hand-off: `prukka up` and the tray open
// /ui/#token=…; the fragment never travels over the network. The token is
// kept in sessionStorage and editable in the UI as a manual fallback.

const KEY = "prukka-token";

/** isControlToken validates the fixed wire format minted by the daemon. */
export function isControlToken(value: string): boolean {
  return /^[0-9a-f]{64}$/i.test(value);
}

function adopt(): string {
  const fragment = new URLSearchParams(window.location.hash.slice(1));
  const candidate = fragment.get("token") ?? "";
  if (isControlToken(candidate)) {
    write(candidate.toLowerCase());
  }
  if (fragment.has("token")) {
    history.replaceState(null, "", window.location.pathname + window.location.search);
  }

  try {
    return sessionStorage.getItem(KEY) ?? "";
  } catch {
    return isControlToken(candidate) ? candidate.toLowerCase() : "";
  }
}

function write(value: string) {
  try {
    sessionStorage.setItem(KEY, value);
  } catch {
    // The token remains in memory when browser storage is unavailable.
  }
}

class TokenState {
  value = $state(adopt());

  /** set stores the operator-entered token for subsequent writes. */
  set(next: string) {
    const trimmed = next.trim();
    this.value = isControlToken(trimmed) ? trimmed.toLowerCase() : trimmed;
    write(this.value);
  }
}

export const token = new TokenState();
