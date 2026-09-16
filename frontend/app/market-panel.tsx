"use client";

import { useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from "react";
import { createPublicClient, createWalletClient, custom, http, parseAbi, type Address } from "viem";
import { erc20Abi } from "viem";

const ticks = 60;
const marketAddress = process.env.NEXT_PUBLIC_MARKET_ADDRESS as Address | undefined;
const usdcAddress = process.env.NEXT_PUBLIC_MOCK_USDC_ADDRESS as Address | undefined;
const rpcUrl = process.env.NEXT_PUBLIC_ANVIL_RPC_URL ?? "http://127.0.0.1:8545";
const anvil = { id: 31337, name: "Anvil", nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 }, rpcUrls: { default: { http: [rpcUrl] } } } as const;
const marketAbi = parseAbi([
  "function placeRangeBet(uint256 seriesId, uint256 roundId, uint16 firstBucket, uint16 lastBucket, uint256 amount) returns (uint256)",
  "function claim(uint256 ticketId) returns (uint256)",
  "function claimRefund(uint256 ticketId) returns (uint256)",
]);
const seriesId = BigInt(process.env.NEXT_PUBLIC_MARKET_SERIES_ID ?? "1");
const roundId = BigInt(process.env.NEXT_PUBLIC_MARKET_ROUND_ID ?? "1");
// Keep visual values as serialized strings: server and client Math implementations
// can differ in the insignificant trailing digits of Math.sin results.
const bars = (() => { let seed = 48271; return Array.from({ length: ticks }, (_, i) => { seed = (seed * 16807) % 2147483647; return `${(24 + (seed % 48) + (Math.sin(i * .44) + 1) * 12).toFixed(4)}%`; }); })();
const label = (index: number) => { const seconds = -150 + index * 5; return `${seconds < 0 ? "−" : "+"}${Math.abs(seconds)}s`; };
const numberValue = (value: string) => { const n = Number(value); return Number.isFinite(n) && n >= 0 ? n : 0; };
declare global { interface Window { ethereum?: { request(args: { method: string; params?: unknown[] }): Promise<unknown> } } }

export function MarketPanel({ serviceRunning }: { serviceRunning: boolean }) {
	const [amount, setAmount] = useState("0.114");
  const [lower, setLower] = useState(21);
  const [upper, setUpper] = useState(39);
	const [feedback, setFeedback] = useState("");
	const [account, setAccount] = useState<Address>();
	const [ticketId, setTicketId] = useState("");
  const graph = useRef<HTMLDivElement>(null);
  const dragging = useRef<"lower" | "upper" | null>(null);
  const entered = numberValue(amount);
	const units = BigInt(Math.round(entered * 1_000_000));
	const width = upper - lower + 1;
	const valid = entered > 0 && units % BigInt(width) === 0n;
  const selected = `${label(lower)} to ${label(upper)}`;
  const updateLower = (value: number) => setLower(Math.max(0, Math.min(upper - 1, value)));
  const updateUpper = (value: number) => setUpper(Math.min(ticks - 1, Math.max(lower + 1, value)));
  const pointerMove = (event: PointerEvent<HTMLDivElement>) => {
    const rect = graph.current?.getBoundingClientRect();
    if (!rect || !dragging.current) return;
    const tick = Math.round(((event.clientX - rect.left) / rect.width) * (ticks - 1));
    dragging.current === "lower" ? updateLower(tick) : updateUpper(tick);
  };
  const pointerDown = (event: PointerEvent<HTMLDivElement>, handle: "lower" | "upper") => { dragging.current = handle; event.currentTarget.setPointerCapture(event.pointerId); pointerMove(event); };
  const keyMove = (event: KeyboardEvent<HTMLButtonElement>, handle: "lower" | "upper") => {
    const current = handle === "lower" ? lower : upper;
    const delta = event.key === "ArrowLeft" || event.key === "ArrowDown" ? -1 : event.key === "ArrowRight" || event.key === "ArrowUp" ? 1 : event.key === "PageDown" ? -5 : event.key === "PageUp" ? 5 : 0;
    if (event.key === "Home") { event.preventDefault(); handle === "lower" ? updateLower(0) : updateUpper(0); return; }
    if (event.key === "End") { event.preventDefault(); handle === "lower" ? updateLower(ticks - 1) : updateUpper(ticks - 1); return; }
    if (delta) { event.preventDefault(); handle === "lower" ? updateLower(current + delta) : updateUpper(current + delta); }
  };
	const quickSize = (fraction: number) => { const allocation = Math.max(1, Math.round(120_000 * fraction)); setAmount(((allocation * width) / 1_000_000).toFixed(6).replace(/0+$/, "").replace(/\.$/, "")); setFeedback(""); };
	const connect = async () => {
		if (!window.ethereum) { setFeedback("No injected wallet found. Connect a browser wallet to Anvil chain 31337."); return; }
		try { const accounts = await window.ethereum.request({ method: "eth_requestAccounts" }) as string[]; setAccount(accounts[0] as Address); setFeedback("Wallet connected. This prototype uses six-decimal mock USDC."); } catch { setFeedback("Wallet connection was declined."); }
	};
	const place = async () => {
		if (!marketAddress || !usdcAddress || !window.ethereum || !account) { setFeedback("Configure contract addresses and connect an Anvil wallet before placing a ticket."); return; }
		try {
			const wallet = createWalletClient({ account, chain: anvil, transport: custom(window.ethereum) });
			const client = createPublicClient({ chain: anvil, transport: http(rpcUrl) });
			setFeedback("Approving mock USDC…");
			await client.waitForTransactionReceipt({ hash: await wallet.writeContract({ address: usdcAddress, abi: erc20Abi, functionName: "approve", args: [marketAddress, units] }) });
			setFeedback("Placing non-transferable range ticket…");
			await client.waitForTransactionReceipt({ hash: await wallet.writeContract({ address: marketAddress, abi: marketAbi, functionName: "placeRangeBet", args: [seriesId, roundId, lower, upper, units] }) });
			setFeedback("Ticket placed. It cannot be sold; claim only after settlement.");
		} catch (error) { setFeedback(error instanceof Error ? error.message : "Transaction failed."); }
	};
	const claim = async (refund: boolean) => {
		if (!marketAddress || !window.ethereum || !account || !/^\d+$/.test(ticketId)) { setFeedback("Enter a ticket ID and connect an Anvil wallet."); return; }
		try { const wallet = createWalletClient({ account, chain: anvil, transport: custom(window.ethereum) }); const client = createPublicClient({ chain: anvil, transport: http(rpcUrl) }); const hash = await wallet.writeContract({ address: marketAddress, abi: marketAbi, functionName: refund ? "claimRefund" : "claim", args: [BigInt(ticketId)] }); await client.waitForTransactionReceipt({ hash }); setFeedback(refund ? "Refund claimed." : "Winning payout claimed."); } catch (error) { setFeedback(error instanceof Error ? error.message : "Claim failed."); }
	};
  const graphStyle = { "--range-start": `${lower / (ticks - 1) * 100}%`, "--range-end": `${upper / (ticks - 1) * 100}%` } as CSSProperties & Record<"--range-start" | "--range-end", string>;
  return <aside className="guess-panel" aria-label="Arrival range market">
	<div className="guess-toolbar"><button type="button" className="wallet-connect" onClick={() => void connect()}>{account ? `${account.slice(0, 6)}…${account.slice(-4)}` : "Connect Anvil"}</button></div>
	<p className="market-overline">Arrival range</p>
	<label className="amount-label" htmlFor="guess-amount">Amount <span>Mock USDC · split evenly across each selected bucket</span></label>
	<div className="amount-box"><input id="guess-amount" inputMode="decimal" value={amount} onChange={(event) => { setAmount(event.target.value); setFeedback(""); }} aria-describedby="amount-help" /><span aria-hidden="true">mUSDC</span><div className="quick-sizes" aria-label="Quick amount selection">{[.05, .1, .25, .5].map((fraction) => <button key={fraction} type="button" onClick={() => quickSize(fraction)}>{fraction * 100}%</button>)}<button type="button" onClick={() => quickSize(1)}>MAX</button></div></div>
	<p className={`amount-help${amount !== "" && !valid ? " is-error" : ""}`} id="amount-help">{amount !== "" && !valid ? `Choose an amount divisible by ${width} selected buckets at six-decimal precision.` : `${(Number(units) / width / 1_000_000).toFixed(6)} mUSDC allocated to each bucket.`}</p>
    <div className="range-heading"><div><span>Liquidity range</span><strong>{selected}</strong></div><span>{upper - lower + 1} ticks</span></div>
    <div className="range-chart" ref={graph} style={graphStyle} onPointerMove={pointerMove} onPointerUp={() => { dragging.current = null; }} onPointerCancel={() => { dragging.current = null; }}><div className="range-selection" aria-hidden="true" /><div className="current-marker" aria-label="Current estimate" /><div className="range-bars" aria-hidden="true">{bars.map((height, index) => <span key={index} className={index >= lower && index <= upper ? "is-selected" : ""} style={{ height }} />)}</div><div className="range-handle" style={{ left: `calc(${lower / (ticks - 1) * 100}% - 8px)` }} onPointerDown={(event) => pointerDown(event, "lower")}><button type="button" role="slider" aria-label="Range start" aria-valuemin={0} aria-valuemax={ticks - 1} aria-valuenow={lower} aria-valuetext={label(lower)} onKeyDown={(event) => keyMove(event, "lower")} /></div><div className="range-handle" style={{ left: `calc(${upper / (ticks - 1) * 100}% - 8px)` }} onPointerDown={(event) => pointerDown(event, "upper")}><button type="button" role="slider" aria-label="Range end" aria-valuemin={0} aria-valuemax={ticks - 1} aria-valuenow={upper} aria-valuetext={label(upper)} onKeyDown={(event) => keyMove(event, "upper")} /></div></div>
    <div className="range-labels"><span>Min {label(0)}</span><strong>Selected {selected}</strong><span>Max {label(ticks - 1)}</span></div>
	<button type="button" className="place-guess" disabled={!serviceRunning || !valid} onClick={() => void place()}>Approve & place range ticket</button><p className="guess-feedback" aria-live="polite">{feedback || "Claims are pull-based after the oracle settles or refunds the round."}</p>
	<div className="claim-controls"><input aria-label="Ticket ID" inputMode="numeric" placeholder="Ticket ID" value={ticketId} onChange={(event) => setTicketId(event.target.value)} /><button type="button" onClick={() => void claim(false)}>Claim winner</button><button type="button" onClick={() => void claim(true)}>Claim refund</button></div>
  </aside>;
}
