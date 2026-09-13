import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import CodebaseChat from "./CodebaseChat.jsx";
import Connections from "./Connections.jsx";
import { emptyState } from "./bridge.js";

const preset = {id:"codebase-fixture",name:"Codebase fixture",transport:"stdio",command:"/operator/codebase",directory:"/project",codebaseCache:"/private-work/cache",allowedTools:["list_projects"]};
function fixture() {
 const state=emptyState(); state.settings.mode="local";
 state.settings.connections=[preset,{id:"http",name:"External HTTP",transport:"streamable-http",url:"http://127.0.0.1:3000"},{id:"plain",name:"Other MCP",transport:"stdio",command:"/operator/mcp"}];
 return state;
}
describe("Codebase-only chat",()=>{
 it("does not offer generic stdio or HTTP in chat",()=>{
  const action=vi.fn();render(<CodebaseChat state={fixture()} selected="http" onSelect={vi.fn()} onAction={action}/>);
  expect(screen.queryByRole("option",{name:/External HTTP|Other MCP/})).not.toBeInTheDocument();
  expect(screen.queryByRole("button",{name:"取得 Codebase 工具清單"})).not.toBeInTheDocument();
  expect(action).not.toHaveBeenCalled();
 });
 it.each(["awaiting_confirmation","completed","failed","superseded"])("confirms only a current request: %s",(status)=>{
  const state=fixture(),action=vi.fn();
  state.sourceChat={id:"read-id",connectionID:preset.id,status,question:"read source",expiresAt:new Date(Date.now()+60000).toISOString(),advice:{reason:"check first",tool:{name:"list_projects"},argumentsJSON:"{}"}};
  render(<CodebaseChat state={state} selected={preset.id} onSelect={vi.fn()} onAction={action}/>);
  const button=screen.getByRole("button",{name:"確認本次讀取（不入庫）"});
  if(status==="awaiting_confirmation") {expect(button).toBeEnabled();fireEvent.click(button);expect(action).toHaveBeenCalledWith("ConfirmSourceChat","read-id");}
  else {expect(button).toBeDisabled();fireEvent.click(button);expect(action).not.toHaveBeenCalled();}
 });
 it("expired and busy cards cannot confirm",()=>{
  const state=fixture(),action=vi.fn();state.sourceChat={id:"read-id",connectionID:preset.id,status:"awaiting_confirmation",expiresAt:"2000-01-01T00:00:00Z",advice:{tool:{name:"list_projects"}}};
  render(<CodebaseChat state={state} disabled selected={preset.id} onSelect={vi.fn()} onAction={action}/>);
  for(const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
 });
 it("folder selection only adds a draft and never indexes or saves",async()=>{
  const state=emptyState(),action=vi.fn();
  const choose=vi.fn().mockResolvedValueOnce({}).mockResolvedValue(preset);
  window.go={main:{App:{Snapshot:vi.fn(),ChooseCodebaseRepository:choose}}};
  function Harness(){const[draft,setDraft]=React.useState(state.settings);return <Connections state={state} draft={draft} onDraftChange={setDraft} onAction={action}/>;}
  render(<Harness/>);
  const button=screen.getByRole("button",{name:"加入本機 Codebase（選擇資料夾）"});
  fireEvent.click(button);await vi.waitFor(()=>expect(button).toBeEnabled());
  expect(screen.queryByText("Codebase fixture")).not.toBeInTheDocument();
  fireEvent.click(button);await screen.findByRole("heading",{name:"Codebase fixture"});
  fireEvent.click(button);await vi.waitFor(()=>expect(choose).toHaveBeenCalledTimes(3));
  expect(screen.getAllByRole("heading",{name:"Codebase fixture"})).toHaveLength(1);
  expect(screen.getByText("進階設定（固定預設）").parentElement).not.toHaveAttribute("open");
  expect(screen.getByRole("button",{name:"建立／更新此資料夾索引（不入庫）"})).toBeDisabled();
  expect(action).not.toHaveBeenCalled();delete window.go;
 });
});
