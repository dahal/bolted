import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";
import { BoltIcon } from "@/components/bolt-icon";
import { appName, gitConfig } from "./shared";

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      // Brand mark: bolt glyph (accent-coloured via currentColor) + wordmark
      // in DM Mono Medium, tracking-tight, slightly larger than the nav
      // default. The icon picks up --color-fd-primary through the text-fd-primary
      // class on the wrapper.
      title: (
        <span className="flex items-center gap-2 font-mono font-bold text-lg tracking-tight">
          <BoltIcon className="size-6 text-fd-primary" />
          {appName}
        </span>
      ),
    },
    githubUrl: `https://github.com/${gitConfig.user}/${gitConfig.repo}`,
  };
}
