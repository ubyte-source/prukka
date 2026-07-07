// Control-token hand-off: `prukka up` and the tray open
// /ui/#token=…; the fragment never travels over the network. The token is
// kept in sessionStorage and editable in the UI as a manual fallback.

const KEY = "prukka-token";

function adopt(): string {
  const match = window.location.hash.match(/token=([0-9a-f]+)/);
  if (match) {
    sessionStorage.setItem(KEY, match[1]);
    history.replaceState(null, "", window.location.pathname);
  }

  return sessionStorage.getItem(KEY) ?? "";
}

class TokenState {
  value = $state(adopt());

  /** set stores the operator-entered token for subsequent writes. */
  set(next: string) {
    this.value = next.trim();
    sessionStorage.setItem(KEY, this.value);
  }
}

export const token = new TokenState();
