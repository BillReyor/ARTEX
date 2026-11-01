"use client";

import * as React from "react";

import { CheckIcon, Languages } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useLocale } from "@/lib/i18n/store";
import { cn } from "@/lib/utils";

type LanguageToggleProps = {
  className?: string;
};

export function LanguageToggle({ className }: LanguageToggleProps) {
  const { locale, setLocale } = useLocale();

  const isZh = locale === "zh";
  const [applying, setApplying] = React.useState(false);

  const handleSelect = (target: "en" | "zh") => {
    // Write directly to localStorage in the shape expected by zustand persist
    const storageValue = JSON.stringify({ state: { locale: target }, version: 0 });
    try {
      window.localStorage.setItem("artex-locale", storageValue);
    } catch (_e) {
      // ignore storage errors (e.g., in non-browser env)
    }
    setLocale(target);
    setApplying(true);
    // Reload once
    window.location.reload();
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="Switch language"
          className={cn(
            "relative dark:aria-expanded:bg-transparent dark:hover:aria-expanded:bg-transparent",
            className,
          )}
          disabled={applying}
        >
          <Languages className="size-4" />
          <span
            className={cn(
              "pointer-events-none absolute -right-0.5 -bottom-0.5 rounded border border-foreground/20 bg-foreground px-0.5 font-semibold text-[9px] text-background leading-none",
            )}
          >
          {isZh ? "ZH" : "EN"}
          </span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={() => handleSelect("en")} inset>
          English
          {locale === "en" && <CheckIcon className="ml-auto size-4" />}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => handleSelect("zh")} inset>
          Chinese
          {locale === "zh" && <CheckIcon className="ml-auto size-4" />}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
