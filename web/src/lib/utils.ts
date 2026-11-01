import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// The onInteractOutside of the drawer/dialog (Sheet/Dialog) turns off the judgment assistance.
//
// Background: Radix pop-up layers (Select drop-down, DropdownMenu, Popover, etc.) in the drawer will be portaled to the drawer
// outside. If you click outside the mask/drawer when the pop-up layer is open and want to close it, this time the pointerdown will be used by Select and Sheet.
// DismissableLayer is processed at the same time; Select is closed first and is a discrete event, React will flush synchronously, so
// When it is Sheet's processor's turn, the data-state of the pop-up layer has already been turned to closed - detecting whether the pop-up layer is open "at the moment"
// Naturally unreliable (proven by actual measurements).
//
// Correct approach: Radix's pointerdown monitoring is in the bubbling phase; we first put it in the capture phase (earlier than it)
// "Is there any elastic layer open at this moment" is recorded, and onInteractOutside reads this record value to decide whether to release the closure.
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // capture: Record before the pointerdown processor in the Radix bubbling phase
  );
}

// radixOverlayWasOpenAtPointerDown returns "Whether there was a Radix pop-up layer when the last pointerdown occurred"
// On". The drawer/dialog box is based on this: when the pop-up layer is open, click the mask → only close the pop-up layer, but not itself.
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

// copyText writes text to the clipboard and returns whether it is successful.
// Background: navigator.clipboard is only available in a secure context (HTTPS/localhost); via IP + HTTP
// It is undefined when accessed, and is downgraded to execCommand("copy").
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Continue with the downgrade plan
    }
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    textarea.style.top = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

export function formatCurrency(
  amount: number,
  opts?: {
    currency?: string;
    locale?: string;
    minimumFractionDigits?: number;
    maximumFractionDigits?: number;
    noDecimals?: boolean;
  },
) {
  const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts ?? {};

  const formatOptions: Intl.NumberFormatOptions = {
    style: "currency",
    currency,
    minimumFractionDigits: noDecimals ? 0 : minimumFractionDigits,
    maximumFractionDigits: noDecimals ? 0 : maximumFractionDigits,
  };

  return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
