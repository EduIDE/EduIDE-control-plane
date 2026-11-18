import * as vscode from "vscode";
import { AgentClient, type Notice } from "./agent";

let client: AgentClient | undefined;

function show(client: () => AgentClient | undefined, n: Notice) {
  const text = n.at ? `${n.message} (until ${new Date(n.at).toLocaleTimeString()})` : n.message;
  if (n.kind === "info") {
    void vscode.window.showInformationMessage(text);
    return;
  }
  const action = "I am still here";
  void vscode.window.showWarningMessage(text, action).then((choice) => {
    if (choice === action) {
      void client()?.heartbeat(true);
    }
  });
}

export function activate(context: vscode.ExtensionContext) {
  const baseUrl = process.env.EDUIDE_AGENT_URL ?? "http://127.0.0.1:8082";
  const c = new AgentClient({ baseUrl, onNotice: (n) => show(() => client, n) });
  client = c;
  c.start();

  const beat = () => void c.heartbeat();
  context.subscriptions.push(
    vscode.commands.registerCommand("dataBridge.getEnv", (keys: unknown) =>
      Array.isArray(keys) ? c.getEnv(keys.map(String)) : {},
    ),
    vscode.commands.registerCommand("dataBridge.getEnvState", () => c.getEnvState()),
    vscode.workspace.onDidChangeTextDocument((e) => {
      // Output channels and similar are not user input.
      if (e.document.uri.scheme === "file" || e.document.uri.scheme === "untitled") {
        beat();
      }
    }),
    vscode.window.onDidChangeTextEditorSelection(beat),
    vscode.window.onDidChangeActiveTextEditor(beat),
    vscode.window.onDidChangeWindowState((s) => s.focused && beat()),
  );
  // Terminal input events exist only in newer API versions.
  const w = vscode.window as unknown as {
    onDidWriteTerminalData?: (l: () => void) => vscode.Disposable;
  };
  if (w.onDidWriteTerminalData) {
    context.subscriptions.push(w.onDidWriteTerminalData(beat));
  }
}

export async function deactivate() {
  await client?.stop();
  client = undefined;
}
