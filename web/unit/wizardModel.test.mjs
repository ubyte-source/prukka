import assert from "node:assert/strict";
import test from "node:test";

import {
  avSourceUrl,
  detectCallRoles,
  deviceId,
  profileFor,
  slugMax,
  suggestSlug,
  twoWaySlugMax,
  validSlug,
} from "../src/lib/wizard/model.ts";

test("only a call source derives the low-latency call profile", () => {
  assert.equal(profileFor("call"), "call");
  assert.equal(profileFor("mic"), "broadcast");
  assert.equal(profileFor("camera"), "broadcast");
  assert.equal(profileFor("stream"), "broadcast");
});

test("slug validation mirrors the daemon grammar and length bounds", () => {
  assert.ok(validSlug("my-stream", slugMax));
  assert.ok(validSlug("a", slugMax));
  assert.ok(!validSlug("", slugMax));
  assert.ok(!validSlug("-leading", slugMax));
  assert.ok(!validSlug("trailing-", slugMax));
  assert.ok(!validSlug("UPPER", slugMax));
  assert.ok(!validSlug("a".repeat(slugMax + 1), slugMax));
  assert.ok(validSlug("a".repeat(twoWaySlugMax), twoWaySlugMax));
  assert.ok(!validSlug("a".repeat(twoWaySlugMax + 1), twoWaySlugMax));
});

test("suggested names are valid slugs that vary with the random source", () => {
  const first = suggestSlug("mic", () => 0.1);
  const second = suggestSlug("mic", () => 0.9);
  assert.ok(validSlug(first, slugMax), first);
  assert.ok(first.startsWith("mic-"));
  assert.notEqual(first, second);
  assert.ok(validSlug(suggestSlug("call", () => 0), twoWaySlugMax));
});

test("a camera source pairs with its microphone in the device://av grammar", () => {
  assert.equal(deviceId("device://video/cam-1"), "cam-1");
  assert.equal(deviceId("device://audio/mic-2"), "mic-2");
  assert.equal(
    avSourceUrl("device://video/cam-1", "device://audio/mic-2"),
    "device://av/cam-1|mic-2",
  );
});

test("call role detection fills only empty roles, so manual choices survive", () => {
  const devices = [
    { url: "device://audio/speaker-in", label: "Prukka Speaker", kind: "audio-in", virtual: true },
    { url: "device://audio/local-mic", label: "Built-in Microphone", kind: "audio-in" },
    { url: "device://audio/listen", label: "Built-in Output", kind: "audio-out" },
    { url: "device://audio/call-mic", label: "Prukka Microphone", kind: "audio-out", virtual: true },
  ];

  const detected = detectCallRoles(devices, { remote: "", listen: "", mic: "", outgoing: "" });
  assert.deepEqual(detected, {
    remote: "device://audio/speaker-in",
    listen: "device://audio/listen",
    mic: "device://audio/local-mic",
    outgoing: "device://audio/call-mic",
  });

  const manual = detectCallRoles(devices, {
    remote: "",
    listen: "",
    mic: "device://audio/speaker-in",
    outgoing: "",
  });
  assert.equal(manual.mic, "device://audio/speaker-in");
  assert.equal(manual.remote, "device://audio/speaker-in");

  const none = detectCallRoles([], { remote: "", listen: "", mic: "", outgoing: "" });
  assert.deepEqual(none, { remote: "", listen: "", mic: "", outgoing: "" });
});
