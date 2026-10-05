// Recipes: sessions you start often, kept as a list and one keystroke away (⇧⌘R, or by
// name in ⌘K). The same dialog shows the list and the form for one recipe.
import { useEffect, useRef, useState } from "react";
import { Pencil, Play, Plus, SquareTerminal, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { LOCAL, machineLabel, recipeFrom, removeRecipe, runRecipe, saveRecipe, setState, useStore, getState, type Recipe } from "../lib/store";
import { cx, mod, tildePath } from "../lib/util";
import { BrandIcon } from "./Brand";
import { askText } from "./ContextMenu";
import { Button, Checkbox, Field, Input, Kbd, Modal, ModalHeader, Segmented, Select, Textarea } from "./ui";

const SKIP = "--dangerously-skip-permissions";

/** Runs a recipe, first getting what its message asks for: the clipboard, a line from you. */
export async function launchRecipe(r: Recipe, split = false) {
  const text = r.kind === "claude" ? r.prompt : r.run;
  const fill: { clipboard?: string; ask?: string } = {};
  if (text.includes("{clipboard}")) fill.clipboard = (await api.clipboardText().catch(() => "")).trim();
  if (text.includes("{ask}")) {
    const v = await askText({ title: r.name, placeholder: "What goes in {ask}", confirm: "Run" });
    if (v === null) return;
    fill.ask = v;
  }
  setState({ modal: null });
  runRecipe(r, fill, split ? "row" : "group");
}

/** Where a recipe runs, in words. */
export function recipeWhere(r: Recipe): string {
  const where = r.machine ? machineLabel(r.machine) : "where you are";
  return r.dir ? `${where} · ${tildePath(r.dir, getState().info?.home)}` : where;
}

export function RecipesDialog({ edit, onClose }: { edit?: Recipe; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} width={560}>
      {edit ? <RecipeForm initial={edit} /> : <RecipeList />}
    </Modal>
  );
}

function RecipeList() {
  const recipes = useStore((s) => s.recipes);
  const [sel, setSel] = useState(0);
  const box = useRef<HTMLDivElement>(null);
  const create = () => setState({ modal: { type: "recipes", edit: recipeFrom(getState().tabs.find((t) => t.key === getState().activeTab)) } });
  useEffect(() => {
    box.current?.focus();
  }, []);
  return (
    <div
      ref={box}
      tabIndex={-1}
      className="outline-none"
      onKeyDown={(e) => {
        if (e.key === "ArrowDown") setSel((i) => Math.min(recipes.length - 1, i + 1));
        else if (e.key === "ArrowUp") setSel((i) => Math.max(0, i - 1));
        else if (e.key === "Enter" && recipes[sel]) void launchRecipe(recipes[sel], e.altKey);
        else return;
        e.preventDefault();
      }}
    >
      <ModalHeader title="Recipes" subtitle="Sessions you start often: a machine, a folder, and what to start with." />
      <div className="px-2 pb-2">
        {recipes.length === 0 && <div className="px-3 py-6 text-center text-[12.5px] text-subtle">Nothing yet. Make one from the pane you are in: it starts with that machine and folder.</div>}
        {recipes.map((r, i) => (
          <div
            key={r.id}
            onMouseMove={() => setSel(i)}
            onClick={(e) => void launchRecipe(r, e.altKey)}
            className={cx("group flex h-10 cursor-default items-center gap-3 rounded-lg px-3", i === sel ? "bg-hover" : "")}
          >
            {r.kind === "claude" ? <BrandIcon name="claude" size={15} /> : <SquareTerminal size={15} className="text-subtle" />}
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13px] text-fg">{r.name}</div>
              <div className="truncate text-[11px] text-subtle">
                {recipeWhere(r)}
                {(r.kind === "claude" ? r.prompt : r.run) && <span> · {(r.kind === "claude" ? r.prompt : r.run).replace(/\s+/g, " ").slice(0, 80)}</span>}
              </div>
            </div>
            <div className={cx("flex items-center gap-0.5", i === sel ? "opacity-100" : "opacity-0 group-hover:opacity-100")}>
              <button
                title="Edit"
                onClick={(e) => {
                  e.stopPropagation();
                  setState({ modal: { type: "recipes", edit: r } });
                }}
                className="flex size-6 items-center justify-center rounded text-subtle hover:bg-active hover:text-fg"
              >
                <Pencil size={12} />
              </button>
              <button
                title="Delete"
                onClick={(e) => {
                  e.stopPropagation();
                  removeRecipe(r.id);
                }}
                className="flex size-6 items-center justify-center rounded text-subtle hover:bg-active hover:text-bad"
              >
                <Trash2 size={12} />
              </button>
              <Play size={12} className="ml-1 text-subtle" />
            </div>
          </div>
        ))}
      </div>
      <div className="flex items-center justify-between border-t border-line px-4 py-2.5 text-[11.5px] text-subtle">
        <span className="flex items-center gap-1.5">
          <Kbd>↩</Kbd> run <Kbd>⌥↩</Kbd> in a split <Kbd>{mod}K</Kbd> finds them by name
        </span>
        <Button size="xs" variant="outline" icon={<Plus size={12} />} onClick={create}>
          New recipe
        </Button>
      </div>
    </div>
  );
}

function RecipeForm({ initial }: { initial: Recipe }) {
  const machines = useStore((s) => s.machines);
  const [r, setR] = useState<Recipe>(initial);
  const set = (patch: Partial<Recipe>) => setR((x) => ({ ...x, ...patch }));
  const back = () => setState({ modal: { type: "recipes" } });
  const text = r.kind === "claude" ? r.prompt : r.run;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        saveRecipe(r);
        back();
      }}
    >
      <ModalHeader title={initial.id ? "Edit recipe" : "New recipe"} />
      <div className="flex flex-col gap-3.5 px-5 pb-4">
        <Field label="Name">
          <Input autoFocus value={r.name} placeholder="Review a pull request" onChange={(e) => set({ name: e.target.value })} />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Where">
            <Select
              value={r.machine}
              onChange={(machine) => set({ machine })}
              options={[{ value: "", label: "Where I am" }, { value: LOCAL, label: "This computer" }, ...machines.map((m) => ({ value: m.machine.name, label: m.machine.name }))]}
            />
          </Field>
          <Field label="Folder" hint={r.machine ? undefined : "Empty: the folder of the pane you are in."}>
            <Input mono value={r.dir} placeholder={r.machine ? "~" : "the pane's folder"} onChange={(e) => set({ dir: e.target.value })} />
          </Field>
        </div>
        <Field label="Starts">
          <Segmented<Recipe["kind"]>
            value={r.kind}
            onChange={(kind) => set({ kind })}
            options={[
              { value: "claude", label: "Claude" },
              { value: "shell", label: "A shell" },
            ]}
          />
        </Field>
        {r.kind === "claude" ? (
          <>
            <Field label="First message" hint="Optional. {clipboard} becomes what you last copied (a link, an error); {ask} asks you for a line when the recipe runs.">
              <Textarea rows={5} value={r.prompt} placeholder="Review the pull request at {clipboard}. Check out its branch, run the tests, and tell me what you would change." onChange={(e) => set({ prompt: e.target.value })} />
            </Field>
            <label className="flex items-center gap-2 text-[12.5px] text-muted">
              <Checkbox checked={r.flags.includes(SKIP)} onChange={(on) => set({ flags: on ? SKIP : "" })} />
              Skip permission prompts <span className="font-mono text-[11px] text-subtle">{SKIP}</span>
            </label>
          </>
        ) : (
          <Field label="Command" hint="Optional: typed into the shell once it is up.">
            <Input mono value={r.run} placeholder="npm run dev" onChange={(e) => set({ run: e.target.value })} />
          </Field>
        )}
      </div>
      <div className="flex items-center justify-between border-t border-line px-5 py-3">
        <span className="min-w-0 truncate text-[11.5px] text-subtle">{text.includes("{clipboard}") || text.includes("{ask}") ? "Filled in when it runs." : ""}</span>
        <div className="flex gap-2">
          <Button type="button" variant="ghost" onClick={back}>
            Cancel
          </Button>
          <Button type="submit" variant="primary">
            Save
          </Button>
        </div>
      </div>
    </form>
  );
}
