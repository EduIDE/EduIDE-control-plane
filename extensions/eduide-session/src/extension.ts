import * as vscode from "vscode";
import { AgentClient, type FeedbackPayload, type Notice } from "./agent";

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
  // Loopback only: launch data never comes from anywhere else.
  const c = new AgentClient({ baseUrl: "http://127.0.0.1:8082", onNotice: (n) => show(() => client, n), store: context.globalState,
    // The IDE shows its feedback dialog only when this key is true.
    onFeedbackEnabled: (on) => void vscode.commands.executeCommand("setContext", "eduide.feedbackEnabled", on),
  });
  client = c;
  c.start();
  void c.milestone("plugin-host-started");
  if (vscode.workspace.workspaceFolders?.length) {
    void c.milestone("workspace-ready");
  }

  const beat = () => void c.heartbeat();
  context.subscriptions.push(
    vscode.commands.registerCommand("dataBridge.getEnv", (keys: unknown) =>
      Array.isArray(keys) ? c.getEnv(keys.map(String)) : {},
    ),
    vscode.commands.registerCommand("dataBridge.getEnvState", () => c.getEnvState()),
    vscode.commands.registerCommand("eduide.feedback.send", (payload: FeedbackPayload) => c.sendFeedback(payload)),
    vscode.workspace.onDidChangeWorkspaceFolders(() => {
      if (vscode.workspace.workspaceFolders?.length) {
        void c.milestone("workspace-ready");
      }
    }),
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
}

export async function deactivate() {
  await client?.stop();
  client = undefined;
}
