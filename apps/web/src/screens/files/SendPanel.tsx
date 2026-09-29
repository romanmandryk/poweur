import { useEffect, useMemo, useState } from "react";
import type { TransferState } from "@poweur/client/drive";
import { Copy, Send, Trash2 } from "lucide-react";
import { errorMessage } from "../../actions/relay";
import { browserTransferStats, loadBrowserTransfers, revokeBrowserTransfer, sendBrowserTransfer } from "../../actions/transfers";
import { useSession } from "../../state/session";
import { openPanel, toast } from "../../state/ui";
import { Button, IconButton } from "../../ui/Button";
import { FormGroup, Input, Label, Textarea } from "../../ui/Field";
import { Notice } from "../../ui/Display";

export function openSendPanel() {
  openPanel("Send files", (close) => <SendForm close={close} />);
}

function SendForm({ close }: { close: () => void }) {
  const identity = useSession((state) => state.identity)!;
  const [selected, setSelected] = useState<File[]>([]);
  const [recipients, setRecipients] = useState("");
  const [message, setMessage] = useState("");
  const [password, setPassword] = useState("");
  const [days, setDays] = useState("7");
  const [maxDownloads, setMaxDownloads] = useState("0");
  const [working, setWorking] = useState(false);
  const [progress, setProgress] = useState(0);
  const [result, setResult] = useState<TransferState | null>(null);
  const [history, setHistory] = useState<TransferState[]>([]);
  useEffect(() => { void loadBrowserTransfers(identity).then(setHistory); }, [identity]);
  const total = useMemo(() => selected.reduce((sum, file) => sum + file.size, 0), [selected]);

  if (result) return <TransferResult identity={identity} state={result} setState={(next) => { setResult(next); setHistory((old) => [next, ...old.filter(item => item.id !== next.id)]); }} close={close} />;
  return <div>
    <FormGroup><Label htmlFor="send-files">Files</Label><Input id="send-files" type="file" multiple onChange={(event) => setSelected([...event.currentTarget.files ?? []])} />
      {selected.length > 0 && <p className="mt-2 text-sm text-muted">{selected.map(file => file.name).join(", ")}</p>}</FormGroup>
    <FormGroup><Label htmlFor="send-recipients">Poweur recipients (optional)</Label><Input id="send-recipients" value={recipients} onChange={(event) => setRecipients(event.currentTarget.value)} placeholder="alex.example.com, sam.poweur.net" />
      <p className="mt-1 text-xs text-muted">Recipients receive an encrypted share offer. You always get a private link too.</p></FormGroup>
    <FormGroup><Label htmlFor="send-message">Message (optional)</Label><Textarea id="send-message" className="min-h-24" value={message} onChange={(event) => setMessage(event.currentTarget.value)} /></FormGroup>
    <div className="grid grid-cols-2 gap-3"><FormGroup><Label htmlFor="send-expiry">Expires</Label><select id="send-expiry" value={days} onChange={(event) => setDays(event.currentTarget.value)} className="input w-full rounded-control border-[1.5px] border-sep bg-surface px-4 py-[13px]"><option value="1">1 day</option><option value="7">7 days</option><option value="30">30 days</option><option value="90">90 days</option></select></FormGroup>
      <FormGroup><Label htmlFor="send-downloads">Download cap</Label><Input id="send-downloads" type="number" min="0" value={maxDownloads} onChange={(event) => setMaxDownloads(event.currentTarget.value)} /></FormGroup></div>
    <FormGroup><Label htmlFor="send-password">Password (optional)</Label><Input id="send-password" type="password" autoComplete="new-password" value={password} onChange={(event) => setPassword(event.currentTarget.value)} /></FormGroup>
    {working && <div className="mb-4"><div className="mb-1 flex justify-between text-xs text-muted"><span>Encrypting and uploading</span><span>{progress}%</span></div><div className="h-1.5 overflow-hidden rounded-full bg-surface-3" role="progressbar" aria-valuenow={progress}><div className="h-full bg-accent" style={{ width: `${progress}%` }} /></div></div>}
    <Button id="btn-create-transfer" disabled={!selected.length || working} onClick={() => {
      setWorking(true); setProgress(0);
      const expiresAt = new Date(Date.now() + Number(days) * 86_400_000).toISOString().replace(/\.\d{3}Z$/, "Z");
      void sendBrowserTransfer(identity, selected, { expiresAt, password, maxDownloads: Number(maxDownloads) || 0, message,
        recipients: recipients.split(/[\s,]+/).map(value => value.trim().toLowerCase()).filter(Boolean),
        onState(next) { const sent = next.files.reduce((sum, file) => sum + file.offset, 0); setProgress(total ? Math.round(sent / total * 100) : 100); },
      }).then(({ state, notified }) => { setResult(state); setHistory((old) => [state, ...old.filter(item => item.id !== state.id)]); toast(notified ? `Transfer ready and sent to ${notified} recipient${notified === 1 ? "" : "s"}` : "Transfer ready", "success"); },
        (cause) => toast(errorMessage(cause), "error", 7000)).finally(() => setWorking(false));
    }}><Send className="size-4" />Create transfer</Button>
    {history.length > 0 && <section className="mt-6 border-t border-sep pt-4"><h3 className="mb-2 text-sm font-semibold">Recent transfers</h3>{history.map(item => <button type="button" key={item.id} className="mb-2 w-full rounded-control bg-surface-2 p-3 text-left" onClick={() => setResult(item)}><span className="block truncate font-semibold">{item.files.map(file => file.name).join(", ")}</span><span className="text-xs text-muted">{item.status} · expires {item.expires_at.slice(0, 10)}</span></button>)}</section>}
  </div>;
}

function TransferResult({ identity, state, setState, close }: { identity: string; state: TransferState; setState: (state: TransferState) => void; close: () => void }) {
  const [busy, setBusy] = useState(false);
  const [opens, setOpens] = useState<number | null>(null);
  useEffect(() => { if (state.share) void browserTransferStats(identity, state).then(value => setOpens(value.opens), () => {}); }, [identity, state]);
  return <div><Notice>{state.files.length} file{state.files.length === 1 ? "" : "s"} encrypted and ready until {state.expires_at.slice(0, 10)}.</Notice>
    {state.status === "ready" && <div className="mt-4 rounded-control bg-surface-2 p-3"><div className="text-xs text-muted">Recipient opens</div><div className="text-xl font-semibold">{opens ?? "—"}</div></div>}
    <FormGroup className="mt-4"><Label htmlFor="transfer-link">Private link</Label><div className="flex gap-2"><Input id="transfer-link" readOnly value={state.url ?? ""} /><IconButton aria-label="Copy transfer link" onClick={() => void navigator.clipboard?.writeText(state.url ?? "").then(() => toast("Link copied", "success"))}><Copy className="size-4" /></IconButton></div></FormGroup>
    <div className="flex gap-2">{state.status === "ready" && <Button variant="danger" disabled={busy} onClick={() => { setBusy(true); void revokeBrowserTransfer(identity, state).then((next) => { setState(next); toast("Transfer revoked and files deleted", "success"); }, (cause) => toast(errorMessage(cause), "error", 7000)).finally(() => setBusy(false)); }}><Trash2 className="size-4" />Revoke</Button>}<Button onClick={close}>Done</Button></div>
  </div>;
}
