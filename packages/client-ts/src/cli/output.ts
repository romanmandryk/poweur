/** Terminal output helpers shared by every command. */

export interface Streams {
  stdout: (text: string) => void;
  stderr: (text: string) => void;
}

export function defaultStreams(): Streams {
  return {
    stdout: (text) => process.stdout.write(text),
    stderr: (text) => process.stderr.write(text),
  };
}

/**
 * Print either pretty JSON (`--json`, for piping into other tools) or a
 * human-readable line. Mirrors the Go CLI's `writeOutput`.
 */
export function write(
  streams: Streams,
  jsonOut: boolean,
  payload: unknown,
  message: string,
): number {
  if (jsonOut) {
    streams.stdout(`${JSON.stringify(payload, null, 2)}\n`);
    return 0;
  }
  streams.stdout(message);
  return 0;
}

export function fail(streams: Streams, error: unknown): number {
  const message = error instanceof Error ? error.message : String(error);
  streams.stderr(`${message}\n`);
  return 1;
}
