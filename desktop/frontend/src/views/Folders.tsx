// Projects: one list of the folders you work in, on every device (components/RecentFolders),
// and cloning a repo onto a machine.
import { useState } from "react";
import { GitBranch, RefreshCw } from "lucide-react";
import { api } from "../lib/api";
import { openPaneIn } from "../lib/projects";
import { runOp, toast, useStore, waitOp } from "../lib/store";
import { baseName } from "../lib/util";
import { Button, Field, IconButton, Input, Modal, ModalHeader, Page, Select } from "../components/ui";
import { BrandIcon } from "../components/Brand";
import { RecentFolders } from "../components/RecentFolders";

export function FoldersView() {
  const machines = useStore((s) => s.machines);
  const usable = machines.filter((m) => m.machine.status !== "stopped" && m.machine.status !== "missing").map((m) => m.machine.name);
  const [cloning, setCloning] = useState(false);
  const [round, setRound] = useState(0); // a new round reads every device again
  return (
    <Page
      title="Projects"
      subtitle="Your folders on every device. Send one anywhere, Claude conversations included."
      actions={
        <>
          <IconButton label="Read again" onClick={() => setRound((n) => n + 1)}>
            <RefreshCw size={13} />
          </IconButton>
          <Button variant="outline" icon={<GitBranch size={13} />} disabled={usable.length === 0} onClick={() => setCloning(true)}>
            Clone onto a machine
          </Button>
        </>
      }
    >
      <div className="mx-auto max-w-[1180px] px-6 py-5">
        <RecentFolders key={round} fresh={round > 0} />
      </div>
      {cloning && <CloneDialog names={usable} machine={usable[0] ?? ""} onClose={() => setCloning(false)} onDone={() => setRound((n) => n + 1)} />}
    </Page>
  );
}

function CloneDialog({ names, machine: initial, onClose, onDone }: { names: string[]; machine: string; onClose: () => void; onDone: () => void }) {
  const [machine, setMachine] = useState(initial || names[0] || "");
  const [repo, setRepo] = useState("");
  const [dir, setDir] = useState("");
  return (
    <Modal open onClose={onClose} width={460}>
      <ModalHeader title="Clone onto a machine" subtitle="Uses the machine's own GitHub login." onClose={onClose} icon={<BrandIcon name="github" size={16} className="text-fg" />} />
      <form
        className="flex flex-col gap-4 px-5 py-4"
        onSubmit={async (e) => {
          e.preventDefault();
          onClose();
          const id = await runOp(api.clone(machine, repo.trim(), dir.trim()), true);
          const end = await waitOp(id);
          if (!end.error) {
            const path = (end.result as string) || dir || `~/code/${baseName(repo)}`;
            toast("success", `Cloned ${repo}`, `${machine}:${path}`, { label: "Start Claude there", run: () => openPaneIn(machine, path, true) });
            onDone();
          }
        }}
      >
        <Field label="Repository" hint="owner/name, or any git URL.">
          <Input autoFocus mono value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="acme/api" />
        </Field>
        <div className="grid grid-cols-[140px_1fr] gap-3">
          <Field label="Machine">
            <Select value={machine} onChange={setMachine} options={names.map((n) => ({ value: n, label: n }))} />
          </Field>
          <Field label="Into">
            <Input mono value={dir} onChange={(e) => setDir(e.target.value)} placeholder={repo ? `~/code/${baseName(repo).replace(/\.git$/, "")}` : "~/code/<repo>"} />
          </Field>
        </div>
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!repo.trim() || !machine}>
            Clone
          </Button>
        </div>
      </form>
    </Modal>
  );
}
