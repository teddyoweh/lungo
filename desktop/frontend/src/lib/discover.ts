// Machines sky can already see: entries in your ssh config and devices on your tailnet.
import { call } from "./api";

export interface Candidate {
  name: string; // suggested machine name
  source: "ssh" | "tailscale";
  alias?: string;
  host: string;
  user?: string;
  os?: string;
  online: boolean;
  tailscaleIp?: string;
  detail: string;
  otherSync?: string; // another tool already syncs it
}

export const discoverMachines = () => call<Candidate[]>("DiscoverMachines");
export const takeOverSync = (name: string) => call<string>("TakeOverSync", name);
export const otherSync = (name: string) => call<string>("OtherSync", name);

/** Adds a found machine; returns the op id. */
export const addCandidate = (c: Candidate, name: string, takeOver: boolean) =>
  call<string>("AddMachine", { name, alias: c.alias ?? "", host: c.alias ? "" : c.host, user: c.user ?? "", port: 0, keyPath: "", setup: false, tailscale: false, takeOver, tailscaleIp: c.tailscaleIp ?? "" });
