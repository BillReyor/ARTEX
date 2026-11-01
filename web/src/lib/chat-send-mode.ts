"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// The send/line feed key of the session input box. Pure front-end preference: only localStorage, no storage, no synchronization with the account,
// Therefore, changing browsers requires a reset. See issue #39 - 0.3.2 changed Ctrl+Enter to send to Enter.
// Returning the old keys is optional here.
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

export const CHAT_SEND_MODE_OPTIONS: { value: ChatSendMode; label: string }[] = [
  { value: "enter", label: "Enter to send; Shift+Enter for newline" },
  { value: "ctrl-enter", label: "Ctrl+Enter to send; Enter for newline" },
];

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// A collection of subscribers within the same tab. The storage event of localStorage is only triggered on the "Other" tab.
// After making changes to this page in the settings, you need to emit to notify the input box on the same page, otherwise you have to refresh to take effect.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// What is returned is a string literal, Object.is is compared by value, and useSyncExternalStore will not fall into a loop.
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// There is no localStorage on the server, so the default value is rendered first, hydrated and then corrected by getSnapshot.
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey determines whether a keystroke should be sent.
// isComposing / keyCode 229 means that the input method such as Chinese is selecting words and must be allowed, otherwise the word selection by pressing Enter will be sent by mistake.
// The enter mode only excludes Shift, which is consistent with the behavior of 0.3.2 verbatim - the user experience without changing the settings remains unchanged.
// ctrl-enter mode accepts both Ctrl and Cmd (macOS).
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
