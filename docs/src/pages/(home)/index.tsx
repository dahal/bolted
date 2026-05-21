import { Link } from "waku";
import { Card, Cards } from "fumadocs-ui/components/card";

export default function Home() {
  return (
    <div className="flex-1 flex flex-col items-center justify-center text-center px-6 py-12">
      <h1 className="font-semibold text-3xl mb-3">Bolted</h1>
      <p className="text-fd-muted-foreground max-w-xl mb-6">
        A password-locked, encrypted Linux dev environment CLI for Mac and Windows. Run normal dev
        commands prefixed with <code>bolt</code> and they execute inside an isolated, encrypted VM.
      </p>
      <Link
        to="/docs"
        className="px-3 py-2 rounded-lg bg-fd-primary text-fd-primary-foreground font-medium text-sm mx-auto"
      >
        Read the docs
      </Link>

      <div className="w-full max-w-2xl mt-12 text-left">
        <Cards>
          <Card
            href="/docs/quickstart"
            title="Quickstart"
            description="Install, init, unlock, clone, dev - in five minutes."
          />
          <Card
            href="/docs/concepts/encryption"
            title="Encryption"
            description="How locking works and what the threat model covers."
          />
          <Card
            href="/docs/concepts/multi-repo"
            title="Multi-repo runtime"
            description="One VM, many isolated dev containers, shared network."
          />
        </Cards>
      </div>
    </div>
  );
}

export async function getConfig() {
  return {
    render: "static",
  };
}
