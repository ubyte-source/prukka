// The guided wizard's domain: what the user picked, independent of any
// component. The daemon speaks in call/broadcast profiles, the user in
// sources and outputs; profileFor translates between the two vocabularies.

import type { Device } from "../api/types.ts";

/** SourceKind is what the user wants Prukka to listen to. */
export type SourceKind = "mic" | "camera" | "call" | "stream";

export const sourceKinds: readonly SourceKind[] = ["mic", "camera", "call", "stream"];

/** profileFor derives the daemon profile from the chosen source: translating
 *  a live call needs the low-latency call policy; everything else is an
 *  audience feed and gets the broadcast policy with its configurable delay. */
export function profileFor(kind: SourceKind): "call" | "broadcast" {
  return kind === "call" ? "call" : "broadcast";
}

/** The daemon's slug grammar and length bounds (a two-way call appends
 *  "-in"/"-out" to the name, so both lanes must fit the 63-byte limit). */
export const slugPattern = "[a-z0-9](?:[a-z0-9-]*[a-z0-9])?";
export const slugMax = 63;
export const twoWaySlugMax = 59;

/** validSlug mirrors the daemon's session-name grammar. */
export function validSlug(name: string, max: number): boolean {
  return name.length <= max && new RegExp(`^${slugPattern}$`).test(name);
}

/** suggestSlug proposes an editable session name from the source kind, with a
 *  short random suffix so consecutive sessions never collide. */
export function suggestSlug(kind: SourceKind, random: () => number = Math.random): string {
  const suffix = Math.floor(random() * 36 ** 4).toString(36).padStart(4, "0");
  return `${kind}-${suffix}`;
}

/** deviceId strips the scheme prefix from an enumerated device URL. */
export function deviceId(url: string): string {
  return url.replace(/^device:\/\/(audio|video)\//, "");
}

/** avSourceUrl pairs a camera with its microphone, the daemon's
 *  device://av/<camera>|<mic> grammar. */
export function avSourceUrl(camera: string, mic: string): string {
  return `device://av/${deviceId(camera)}|${deviceId(mic)}`;
}

/** DeviceRoles are the canonical call devices: Prukka's virtual pair matched
 *  by label, the real microphone and output by not being virtual. */
export interface DeviceRoles {
  /** remote is where Prukka hears the other side (the virtual speaker). */
  remote: string;
  /** listen is where the user hears the translation (a real output). */
  listen: string;
  /** mic is the user's real microphone. */
  mic: string;
  /** outgoing is the virtual microphone the call app reads. */
  outgoing: string;
}

/** detectCallRoles fills only the empty roles, so a manual choice survives
 *  every periodic device refresh. */
export function detectCallRoles(devices: readonly Device[], current: DeviceRoles): DeviceRoles {
  const captures = devices.filter((device) => device.kind === "audio-in");
  const outputs = devices.filter((device) => device.kind === "audio-out");
  const remote = captures.find((device) => device.label.includes("Prukka Speaker"));
  const listen = outputs.find((device) => !device.virtual);
  const mic = captures.find((device) => !device.virtual);
  const outgoing = outputs.find((device) => device.label.includes("Prukka Microphone"));

  return {
    remote: current.remote === "" ? (remote?.url ?? "") : current.remote,
    listen: current.listen === "" ? (listen?.url ?? "") : current.listen,
    mic: current.mic === "" ? (mic?.url ?? "") : current.mic,
    outgoing: current.outgoing === "" ? (outgoing?.url ?? "") : current.outgoing,
  };
}
