import type { ReactNode } from "react";
import { Link } from "waku";
import { Accordion, Accordions } from "fumadocs-ui/components/accordion";
import { BoltIcon } from "@/components/bolt-icon";
import { CopyCommand } from "@/components/copy-command";

// ─── Content ────────────────────────────────────────────────────────────────

const INSTALL_CMD =
  "curl -fsSL https://raw.githubusercontent.com/dahal/bolted/main/install.sh | sh";

// Real, public, named supply-chain incidents from the last ~24 months.
// Each one is something a developer would have actually heard of; the
// containment statement keeps to what Bolted's boundary actually does, not
// what it can't.
const ATTACKS: ReadonlyArray<{
  date: string;
  name: string;
  reference: string;
  summary: string;
  contain: string;
}> = [
  {
    date: "Mar 2025",
    name: "tj-actions/changed-files",
    reference: "CVE-2025-30066",
    summary:
      "Compromised GitHub Action used in 23,000+ repos exfiltrated CI/CD secrets directly from runner memory before maintainers noticed.",
    contain:
      "Tokens you used inside Bolted (npm, gh, gcloud, kubectl) live in Bolted. They're not in your shell history, not in your OS keychain, and not in any environment a hijacked third-party action can read.",
  },
  {
    date: "Mar 2024",
    name: "XZ Utils backdoor",
    reference: "CVE-2024-3094",
    summary:
      "Multi-year social-engineering of the xz/liblzma maintainer planted an OpenSSH RCE that was caught by a Microsoft engineer ~30 days before stable release.",
    contain:
      "Every package you build against runs inside Bolted, against Bolted's OpenSSH and libraries. The sshd, the SSH keys, and the agent on your computer are in a different machine the attacker never reaches.",
  },
  {
    date: "Jun 2024",
    name: "polyfill.io takeover",
    reference: "Polyfill CDN supply-chain",
    summary:
      "After a quiet domain sale, the popular JS polyfill CDN began injecting malware into the ~100,000 sites still loading it from <code>cdn.polyfill.io</code>.",
    contain:
      "If a CDN script ends up running in a dev container, the only thing it sees is that container. Other repos on the same Bolted instance, your browser cookies, and your password manager are all on the other side of the boundary.",
  },
  {
    date: "Dec 2023",
    name: "Ledger Connect Kit",
    reference: "ledger-connect-kit hijack",
    summary:
      "A phished Ledger employee account pushed a wallet-draining update to <code>@ledgerhq/connect-kit</code>; ~$600k drained in under an hour.",
    contain:
      "A wallet drainer that lands inside Bolted only sees what's inside Bolted. Your browser extensions, your OS-level wallet, your TouchID-protected keychain — none of them are reachable from a dev container.",
  },
];

// What Bolted's threat model actually addresses, in three statements.
const PROTECTIONS: ReadonlyArray<{ title: string; body: string }> = [
  {
    title: "Bolted can't corrupt your computer",
    body: "Malicious postinstall scripts, compromised npm/pip/cargo/brew packages, AI agents that 'just run a command' — the blast radius is exactly one container, with no path to your shell, SSH keys, OS keychain, or browser.",
  },
  {
    title: "Your computer can't corrupt Bolted",
    body: "A backdoored editor extension or a CLI you brew-installed can't read or modify code, dependencies, or secrets that live inside Bolted. Your computer sees one opaque disk image and nothing more.",
  },
  {
    title: "At rest, it's ciphertext",
    body: "A lost laptop is a brick. The volume is unlocked by an Argon2id-derived key from a password only you know. `bolt lock` evicts the key from kernel memory the moment you walk away.",
  },
];

// Threats Bolted doesn't address — kept up front so the security promises
// stay credible.
const NOT_PROTECTED: ReadonlyArray<string> = [
  "A keylogger already running on your computer can capture the password as you type it. Bolted encrypts data at rest; it does not heal a compromised endpoint.",
  "Cold-boot and DMA attacks against an unlocked machine. The LUKS key sits in kernel memory while Bolted is unlocked.",
  "An offline brute force against a backup of the encrypted volume + a weak password. Argon2id makes the work expensive, not impossible. Pick a strong passphrase.",
  "Whatever runs inside one of your dev containers. A wallet drainer that lands in your container is still draining whatever it can reach from there — it just can't pivot to your real wallet or to other repos.",
];

const FAQ: ReadonlyArray<{ q: string; a: ReactNode }> = [
  {
    q: "Is this just a VM?",
    a: (
      <>
        Yes — Lima on Mac, WSL2 on Windows. You don't see it; you see{" "}
        <code className="font-mono text-sm">bolt</code>. The point isn't novel virtualization, it's
        a usable workflow that puts a real boundary between your editor and the code you don't yet
        trust.
      </>
    ),
  },
  {
    q: "Will my builds be slower?",
    a: (
      <>
        Disk is native — no virtiofs / 9p shenanigans — so cold builds run within
        single-digit-percent of running directly on macOS in our benchmarks. Cargo / pnpm / pip
        caches live inside Bolted so warm builds are faster than a fresh Docker container.
      </>
    ),
  },
  {
    q: "How is this different from Docker?",
    a: (
      <>
        Docker runs containers; it doesn't make a decision about <em>where</em> the dev environment
        lives. Bolted is the encrypted Linux machine those containers run inside. You can absolutely
        use Docker (or, as we do by default, podman) inside Bolted — that's the supported
        devcontainer path.
      </>
    ),
  },
  {
    q: "Production-ready?",
    a: (
      <>
        Pre-release. <code className="font-mono text-sm">v0.1.0</code> ships the MVP scope:
        encrypted volume, lock/unlock, multi-repo, devcontainer dev / exec / stop, password
        rotation. Roadmap items each have an explicit spec in the project.
      </>
    ),
  },
  {
    q: "Open source?",
    a: (
      <>
        Apache 2.0. No telemetry, no analytics, no auth service. The code is small enough to audit
        in an afternoon — that's deliberate.
      </>
    ),
  },
  {
    q: "Linux support?",
    a: (
      <>
        Post-MVP. The current targets are macOS (via Lima) and Windows (via WSL2) — the platforms
        where "a sealed Linux dev environment" is most useful. A native Linux backend isn't far off,
        it's just not the priority.
      </>
    ),
  },
];

// ─── Page ───────────────────────────────────────────────────────────────────

export default function Home() {
  return (
    <main className="w-full">
      <Hero />
      <Section number="01" title="Attacks Bolted would have contained">
        <p className="mt-6 max-w-3xl text-fd-muted-foreground">
          Real, public, named incidents from the last 24 months. Each describes what happened and
          where Bolted's boundary would have drawn the blast radius.
        </p>
        <div className="mt-10 grid gap-5 md:grid-cols-2">
          {ATTACKS.map((a) => (
            <AttackCard key={a.reference} {...a} />
          ))}
        </div>
      </Section>

      <Section number="02" title="What it protects">
        <div className="mt-10 grid gap-5 md:grid-cols-3">
          {PROTECTIONS.map((p) => (
            <ProtectionCard key={p.title} {...p} />
          ))}
        </div>
      </Section>

      <Section number="03" title="Get going">
        <p className="mt-6 max-w-3xl text-fd-muted-foreground">
          Four shell commands from nothing installed to a dev container running inside an encrypted
          volume. About five minutes, depending on how fast your network is.
        </p>
        <div className="mt-10 flex max-w-3xl flex-col gap-6">
          <Step n="1" desc="Install the CLI.">
            <CopyCommand command={INSTALL_CMD} />
          </Step>
          <Step n="2" desc="Create the encrypted volume and VM (asks for a password — twice).">
            <CopyCommand command="bolt init" />
          </Step>
          <Step n="3" desc="Clone a repo into the volume.">
            <CopyCommand command="bolt git clone https://github.com/your-org/your-repo.git" />
          </Step>
          <Step n="4" desc="Spin up the dev container.">
            <CopyCommand command="bolt dev your-repo" />
          </Step>
        </div>
        <div className="mt-10">
          <CTA to="/docs/quickstart">Full quickstart →</CTA>
        </div>
      </Section>

      <Section number="04" title="What Bolted does not protect">
        <p className="mt-6 max-w-3xl text-fd-muted-foreground">
          Honesty matters in a security tool. The threats Bolted's design does not address:
        </p>
        <ul className="mt-10 grid max-w-3xl gap-5">
          {NOT_PROTECTED.map((s, i) => (
            <li key={s} className="flex gap-4 border-l-2 border-fd-border pl-4">
              <span className="pt-1 font-mono text-xs uppercase tracking-widest text-fd-muted-foreground">
                {String(i + 1).padStart(2, "0")}
              </span>
              <span className="text-fd-foreground/90">{s}</span>
            </li>
          ))}
        </ul>
      </Section>

      <Section number="05" title="FAQ">
        <div className="mt-10 max-w-3xl">
          <Accordions type="single">
            {FAQ.map((item, i) => (
              <Accordion key={i} title={item.q} value={`faq-${i}`} className="font-sans">
                {item.a}
              </Accordion>
            ))}
          </Accordions>
        </div>
      </Section>

      <FooterCTA />
    </main>
  );
}

// ─── Sub-components ────────────────────────────────────────────────────────

function Hero() {
  return (
    <section className="border-b border-fd-border">
      <div className="mx-auto max-w-5xl px-6 py-20 md:py-28">
        <div className="mb-10 flex items-center gap-3 text-fd-muted-foreground">
          <BoltIcon className="size-5 text-fd-primary" />
          <span className="font-mono text-xs uppercase tracking-widest">v0.1.0 — pre-release</span>
        </div>
        <h1 className="font-mono text-5xl font-bold uppercase leading-[0.95] tracking-tight md:text-7xl">
          Encrypted Linux,
          <br />
          <span className="text-fd-primary">bolted shut.</span>
        </h1>
        <p className="mt-8 max-w-2xl text-xl text-fd-muted-foreground">
          A password-locked dev environment for Mac and Windows. A backdoored package, a hijacked AI
          agent, or a compromised laptop can't reach the source you ship.
        </p>
        <div className="mt-12 flex max-w-2xl flex-col gap-4">
          <CopyCommand command={INSTALL_CMD} />
          <div className="flex flex-wrap gap-3">
            <CTA to="/docs/quickstart">Quickstart →</CTA>
            <CTA to="/docs" variant="ghost">
              Read the docs
            </CTA>
            <CTA href="https://github.com/dahal/bolted" external variant="ghost">
              GitHub ↗
            </CTA>
          </div>
        </div>
      </div>
    </section>
  );
}

function FooterCTA() {
  return (
    <section>
      <div className="mx-auto max-w-5xl px-6 py-20">
        <div className="flex flex-col items-baseline justify-between gap-8 border-t border-fd-border pt-14 md:flex-row">
          <div>
            <p className="mb-3 font-mono text-xs uppercase tracking-widest text-fd-muted-foreground">
              Start here
            </p>
            <h3 className="font-mono text-2xl font-bold tracking-tight">
              Five minutes from <code className="text-fd-primary">curl</code> to{" "}
              <code className="text-fd-primary">bolt dev</code>.
            </h3>
          </div>
          <div className="flex flex-wrap gap-3">
            <CTA to="/docs/quickstart">Quickstart →</CTA>
            <CTA href="https://github.com/dahal/bolted" external variant="ghost">
              GitHub ↗
            </CTA>
          </div>
        </div>
      </div>
    </section>
  );
}

function Section({
  number,
  title,
  children,
}: {
  number: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <section className="border-b border-fd-border">
      <div className="mx-auto max-w-5xl px-6 py-20">
        <div className="flex items-baseline gap-4 border-b border-fd-border pb-4">
          <span className="font-mono text-xs uppercase tracking-widest text-fd-muted-foreground">
            {number}
          </span>
          <h2 className="font-mono text-2xl font-bold uppercase tracking-tight md:text-3xl">
            {title}
          </h2>
        </div>
        {children}
      </div>
    </section>
  );
}

function AttackCard({
  date,
  name,
  reference,
  summary,
  contain,
}: {
  date: string;
  name: string;
  reference: string;
  summary: string;
  contain: string;
}) {
  return (
    <article className="border border-fd-border bg-fd-card/30 p-6 transition-colors hover:border-fd-primary/40">
      <div className="mb-2 flex items-baseline justify-between gap-4">
        <h3 className="font-mono text-lg font-bold">{name}</h3>
        <time className="font-mono text-xs uppercase tracking-widest text-fd-muted-foreground">
          {date}
        </time>
      </div>
      <p className="mb-4 font-mono text-xs text-fd-muted-foreground">{reference}</p>
      <p className="mb-5 text-fd-foreground/90" dangerouslySetInnerHTML={{ __html: summary }} />
      <p className="border-l-2 border-fd-primary pl-3 text-sm text-fd-foreground/85">
        <span className="mr-1 font-mono text-xs font-bold uppercase tracking-widest text-fd-primary">
          With Bolted
        </span>
        <span
          dangerouslySetInnerHTML={{
            __html: contain,
          }}
        />
      </p>
    </article>
  );
}

function ProtectionCard({ title, body }: { title: string; body: string }) {
  return (
    <div className="border border-fd-border bg-fd-card/30 p-6">
      <h3 className="mb-3 font-mono text-lg font-bold">{title}</h3>
      <p className="text-fd-muted-foreground">{body}</p>
    </div>
  );
}

function Step({ n, desc, children }: { n: string; desc: string; children: ReactNode }) {
  return (
    <div>
      <div className="mb-3 flex items-baseline gap-3">
        <span className="font-mono text-sm font-bold text-fd-primary">{n}.</span>
        <p className="text-fd-foreground/90">{desc}</p>
      </div>
      <div className="ml-6">{children}</div>
    </div>
  );
}

function CTA({
  children,
  to,
  href,
  external,
  variant = "primary",
}: {
  children: ReactNode;
  to?: string;
  href?: string;
  external?: boolean;
  variant?: "primary" | "ghost";
}) {
  const base =
    "inline-flex items-center gap-1.5 px-4 py-2 font-mono text-sm uppercase tracking-wider transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-fd-ring";
  const styles =
    variant === "primary"
      ? "bg-fd-primary text-fd-primary-foreground hover:opacity-90"
      : "border border-fd-border text-fd-foreground hover:border-fd-primary/40 hover:text-fd-primary";
  const className = `${base} ${styles}`;

  if (href && external) {
    return (
      <a href={href} target="_blank" rel="noopener noreferrer" className={className}>
        {children}
      </a>
    );
  }
  if (to) {
    return (
      <Link to={to} className={className}>
        {children}
      </Link>
    );
  }
  return null;
}

export async function getConfig() {
  return {
    render: "static",
  };
}
