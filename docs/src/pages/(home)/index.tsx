import { Link } from 'waku';

export default function Home() {
  return (
    <div className="flex-1 flex flex-col items-center justify-center text-center px-6">
      <h1 className="font-semibold text-3xl mb-3">Bolted</h1>
      <p className="text-fd-muted-foreground max-w-xl mb-6">
        A password-locked, encrypted Linux dev environment CLI for Mac and Windows.
        Run normal dev commands prefixed with <code>bolt</code> and they execute inside an isolated, encrypted VM.
      </p>
      <Link
        to="/docs"
        className="px-3 py-2 rounded-lg bg-fd-primary text-fd-primary-foreground font-medium text-sm mx-auto"
      >
        Read the docs
      </Link>
    </div>
  );
}

export async function getConfig() {
  return {
    render: 'static',
  };
}
