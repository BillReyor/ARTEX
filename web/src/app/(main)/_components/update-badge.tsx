"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * Top bar "new version" notice: checked once on full page load; when an update is
 * available it shows next to the version number, and clicking goes to the
 * "Version & Update" card on the system settings page.
 *
 * The backend caches GitHub query results for 30 minutes, so checking on every mount
 * is safe: the unauthenticated GitHub API only allows 60 requests/hour/IP, and without
 * that cache, opening several tabs could exhaust the quota and hide future updates.
 *
 * Silent on query failure; errors show only when the user clicks "Check for Updates" in settings.
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update already includes the judgment of "comparable version numbers", and this prompt will not be displayed during development builds.
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // Silent: There should be no error in the top bar even if there is no Internet connection/GitHub current limit.
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={`New version found ${latest}; click to update`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* Breathing point: There are many elements in the top bar. Pure text is easy to be ignored, but animation makes it visible at a glance. */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">New version {latest}</span>
      <span className="sm:hidden">New version</span>
    </Link>
  );
}
