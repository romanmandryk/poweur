import { APP_VERSION } from "./build-info";

/** Placeholder shell; replaced by the real decision tree in E21-T4. */
export function App() {
  return (
    <main className="mx-auto flex min-h-full max-w-md flex-col items-center justify-center gap-2 p-6 pt-safe pb-safe">
      <h1 className="text-2xl font-semibold">Poweur ID</h1>
      <p className="text-muted text-sm">next · v{APP_VERSION}</p>
    </main>
  );
}
