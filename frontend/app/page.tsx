"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties } from "react";
import { MarketPanel } from "./market-panel";
import { MarketStatus, type MarketStatusState } from "./market-status";

type TrainProgress = {
  estimated_fraction: number;
  segment_entered_at: string;
  expected_duration_seconds: number;
  method: string;
  confidence: string;
};

type Train = {
  id: string;
  train_number: string;
  direction: string;
  from_station: string;
  to_station: string;
  delay_seconds: number;
  observed_at: string;
  position_kind: "section" | string;
  progress?: TrainProgress;
};

type Snapshot = {
  trains: Train[];
  generated_at?: string;
  stale: boolean;
  age_seconds: number;
  last_error?: string;
};

type ServiceStatus = {
  running: boolean;
  status: "active" | "scheduled_off_hours" | "unknown" | "degraded" | string;
  reason: string;
  observed_at?: string;
  resumes_at?: string;
  generated_at: string;
  source: string;
  confidence: "high" | "medium" | "low" | "none" | string;
};

const stations = [
  "Tokyo", "Kanda", "Akihabara", "Okachimachi", "Ueno", "Uguisudani", "Nippori", "Nishi-Nippori",
  "Tabata", "Komagome", "Sugamo", "Otsuka", "Ikebukuro", "Mejiro", "Takadanobaba", "Shin-Okubo",
  "Shinjuku", "Yoyogi", "Harajuku", "Shibuya", "Ebisu", "Meguro", "Gotanda", "Osaki", "Shinagawa",
  "Takanawa Gateway", "Tamachi", "Hamamatsucho", "Shimbashi", "Yurakucho"
] as const;

const stationCodes = stations.map((_, index) => `JY${String(index + 1).padStart(2, "0")}`);
const mockSnapshot: Snapshot = {
  trains: [{
    id: "demo-loop-01", train_number: "G 1284", direction: "clockwise", from_station: "Meguro", to_station: "Ebisu",
    delay_seconds: 0, observed_at: "2026-01-01T09:42:00Z", position_kind: "section"
  }],
  generated_at: "2026-01-01T09:42:00Z", stale: false, age_seconds: 0
};
const emptySnapshot: Snapshot = { trains: [], stale: false, age_seconds: 0 };
const initialServiceStatus: ServiceStatus = {
  running: false, status: "unknown", reason: "awaiting_service_status", generated_at: "", source: "api", confidence: "none"
};

const liveMode = process.env.NEXT_PUBLIC_TRAIN_LIVE_MODE === "true";
const apiBase = (process.env.NEXT_PUBLIC_TRAIN_API_BASE_URL ?? "").replace(/\/$/, "");
const appContact = process.env.NEXT_PUBLIC_APP_CONTACT || "the app publisher";

type SectionChanged = {
  train_id: string;
  current_segment: { from_station: string; to_station: string; direction: string };
};

type Segment = { origin: number; destination: number };
type WheelTransition = Segment & { leaving: number; key: number };
const demoInitialSegment: Segment = { origin: 21, destination: 20 };

function stationName(value: string) {
  const raw = value.split(":").pop()?.split(".").pop() ?? value;
  return raw.replace(/([a-z])([A-Z])/g, "$1 $2").replace(/[-_]/g, " ").trim();
}

function stationIndex(value: string) {
  const normalized = stationName(value).toLocaleLowerCase();
  return stations.findIndex((station) => station.toLocaleLowerCase() === normalized);
}

function segmentForObservation(fromStation: string, toStation: string, direction: string): Segment | null {
  const origin = stationIndex(fromStation);
  if (origin < 0) return null;
  const reportedDestination = stationIndex(toStation);
  if (reportedDestination >= 0) return { origin, destination: reportedDestination };
  const step = direction.toLocaleLowerCase().includes("outer") ? -1 : 1;
  return { origin, destination: (origin + step + stations.length) % stations.length };
}

function trainOptionLabel(train: Train) {
  const origin = stationName(train.from_station);
  return train.position_kind === "station" || !train.to_station
    ? `${train.train_number} · At ${origin}`
    : `${train.train_number} · ${origin} → ${stationName(train.to_station)}`;
}

function formattedTime(value?: string) {
  if (!value) return "Waiting for a source update";
  const date = new Date(value);
  return Number.isNaN(date.valueOf())
    ? "Waiting for a source update"
    : new Intl.DateTimeFormat("en", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(date);
}

function formattedResumeTime(value?: string) {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return null;
  return `${new Intl.DateTimeFormat("en-US", {
    timeZone: "Asia/Tokyo", hour: "numeric", minute: "2-digit", hour12: true
  }).format(date)} JST`;
}

function delayLabel(seconds: number) {
  if (seconds <= 0) return "On time";
  return `${Math.ceil(seconds / 60)} min late`;
}

function progressLabel(progress: TrainProgress, fraction: number) {
  const duration = Math.max(1, Math.round(progress.expected_duration_seconds / 60));
  return `${Math.round(fraction * 100)}% estimated · ${progress.method.replace(/_/g, " ")} · ${progress.confidence} confidence · ~${duration} min`;
}

function useReducedMotion() {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);
  return reduced;
}

export default function Home() {
  const [index, setIndex] = useState(21);
  const [snapshot, setSnapshot] = useState<Snapshot>(liveMode && apiBase ? emptySnapshot : mockSnapshot);
  const [selectedTrainId, setSelectedTrainId] = useState<string | null>(null);
  const selectedTrainIdRef = useRef<string | null>(null);
  const [transitionProgress, setTransitionProgress] = useState<{ trainId: string; fraction: number } | null>(null);
  const [visibleSegment, setVisibleSegment] = useState<Segment | null>(liveMode && apiBase ? null : demoInitialSegment);
  const visibleSegmentRef = useRef<Segment | null>(liveMode && apiBase ? null : demoInitialSegment);
  const [wheelTransition, setWheelTransition] = useState<WheelTransition | null>(null);
  const [demoFraction, setDemoFraction] = useState(0);
  const wheelTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reducedMotion = useReducedMotion();
  const [connection, setConnection] = useState<"demo" | "connecting" | "live" | "reconnecting" | "unavailable">(
    liveMode && apiBase ? "connecting" : "demo"
  );
  const [serviceStatus, setServiceStatus] = useState<ServiceStatus>(initialServiceStatus);

  const applySnapshot = useCallback((next: Snapshot) => {
    setSnapshot(next);
    const selectedTrain = next.trains.find((train) => train.id === selectedTrainIdRef.current) ?? next.trains[0];
    if (selectedTrain && selectedTrain.id !== selectedTrainIdRef.current) {
      selectedTrainIdRef.current = selectedTrain.id;
      setSelectedTrainId(selectedTrain.id);
    }
    const segment = selectedTrain
      ? segmentForObservation(selectedTrain.from_station, selectedTrain.to_station, selectedTrain.direction)
      : null;
    if (segment) {
      setIndex(segment.origin);
      const visible = visibleSegmentRef.current;
      if (!visible || visible.origin !== segment.origin || visible.destination !== segment.destination) {
        visibleSegmentRef.current = segment;
        setVisibleSegment(segment);
      }
    }
    setTransitionProgress(null);
  }, []);

  const finishWheelTransition = useCallback((segment: Segment) => {
    visibleSegmentRef.current = segment;
    setVisibleSegment(segment);
    setWheelTransition(null);
    setTransitionProgress(null);
  }, []);

  const startWheelTransition = useCallback((next: Segment, trainId?: string) => {
    const previous = visibleSegmentRef.current;
    if (!previous || previous.destination !== next.origin || next.destination === next.origin || reducedMotion) {
      finishWheelTransition(next);
      return;
    }
    if (wheelTimerRef.current) clearTimeout(wheelTimerRef.current);
    if (trainId) setTransitionProgress({ trainId, fraction: 0 });
    setWheelTransition({ ...next, leaving: previous.origin, key: Date.now() });
    wheelTimerRef.current = setTimeout(() => finishWheelTransition(next), 720);
  }, [finishWheelTransition, reducedMotion]);

  useEffect(() => () => {
    if (wheelTimerRef.current) clearTimeout(wheelTimerRef.current);
  }, []);

  useEffect(() => {
    if (!liveMode || !apiBase) return;
    let active = true;
    const load = async () => {
      try {
        const response = await fetch(`${apiBase}/api/v1/trains`, { cache: "no-store" });
        if (!response.ok) throw new Error("Snapshot unavailable");
        const next = (await response.json()) as Snapshot;
        if (active) applySnapshot(next);
      } catch {
        if (active) setConnection("unavailable");
      }
    };
    void load();

    const stream = new EventSource(`${apiBase}/api/v1/trains/stream`);
    stream.addEventListener("snapshot", (event) => {
      try {
        applySnapshot(JSON.parse((event as MessageEvent<string>).data) as Snapshot);
        setConnection("live");
      } catch {
        setConnection("reconnecting");
      }
    });
    stream.addEventListener("train.section_changed", (event) => {
      try {
        const change = JSON.parse((event as MessageEvent<string>).data) as SectionChanged;
        if (change.train_id !== selectedTrainIdRef.current) return;
        const next = segmentForObservation(
          change.current_segment.from_station,
          change.current_segment.to_station,
          change.current_segment.direction
        );
        if (next) {
          setIndex(next.origin);
          startWheelTransition(next, change.train_id);
        }
      } catch {
        setConnection("reconnecting");
      }
    });
    stream.onerror = () => setConnection("reconnecting");
    return () => {
      active = false;
      stream.close();
    };
  }, [applySnapshot, startWheelTransition]);

  useEffect(() => {
    if (!liveMode || !apiBase) return;
    let active = true;
    const loadStatus = async () => {
      try {
        const response = await fetch(`${apiBase}/api/v1/service-status`, { cache: "no-store" });
        if (!response.ok) throw new Error("Service status unavailable");
        const next = (await response.json()) as ServiceStatus;
        if (active) setServiceStatus(next);
      } catch {
        if (active) {
          setServiceStatus({
            running: false, status: "degraded", reason: "service_status_request_failed", generated_at: new Date().toISOString(), source: "api", confidence: "none"
          });
        }
      }
    };
    void loadStatus();
    const timer = window.setInterval(() => void loadStatus(), 5_000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, []);

  const activeTrain = useMemo(
    () => snapshot.trains.find((train) => train.id === selectedTrainId) ?? snapshot.trains[0],
    [selectedTrainId, snapshot.trains]
  );

  const isLive = liveMode && Boolean(apiBase);
  const serviceRunning = !isLive || serviceStatus.running;
  // Only an explicitly active live service opens a live market. The demo keeps
  // its existing mock interaction, while Complete/Forfeited await market data.
  const marketStatus: MarketStatusState = !isLive || (serviceStatus.running && serviceStatus.status === "active")
    ? "In progress"
    : "Bets closed";
  const progressFraction = isLive
    ? Math.max(0, Math.min(1, transitionProgress && transitionProgress.trainId === activeTrain?.id
      ? transitionProgress.fraction
      : activeTrain?.progress?.estimated_fraction ?? 0))
    : demoFraction;
  const selectTrain = (trainId: string) => {
    selectedTrainIdRef.current = trainId;
    setSelectedTrainId(trainId);
    setTransitionProgress(null);
    const train = snapshot.trains.find((item) => item.id === trainId);
    const sectionStart = train ? stationIndex(train.from_station) : -1;
    const sectionEnd = train ? stationIndex(train.to_station) : -1;
    if (sectionStart >= 0) setIndex(sectionStart);
    if (sectionStart >= 0 && sectionEnd >= 0) {
      const segment = { origin: sectionStart, destination: sectionEnd };
      visibleSegmentRef.current = segment;
      setVisibleSegment(segment);
    }
  };
  const statusLabel = connection === "demo" ? "Demo mode" : connection === "live" ? "Live section feed" : connection === "connecting" ? "Connecting" : connection === "reconnecting" ? "Reconnecting" : "Feed unavailable";
  const serviceLabel = serviceStatus.status === "active"
    ? "Service running"
    : serviceStatus.status === "scheduled_off_hours"
      ? "Train not running"
    : serviceStatus.status === "unknown"
        ? "Service status unknown"
        : "Service status degraded";
  const resumeTime = serviceStatus.status === "scheduled_off_hours"
    ? formattedResumeTime(serviceStatus.resumes_at)
    : null;
  const showServiceStatusBanner = isLive && !serviceRunning;
  const trainLabel = activeTrain
    ? `${activeTrain.train_number} — ${delayLabel(activeTrain.delay_seconds)}`
    : "No sections reported";
  const progressDescription = isLive
    ? activeTrain?.progress
      ? progressLabel(activeTrain.progress, progressFraction)
      : "Awaiting a segment estimate"
    : "Demo visualization only";
  const fallbackSegment = useMemo(() => ({ origin: index, destination: (index + 1) % stations.length }), [index]);
  const currentSegment = visibleSegment ?? fallbackSegment;
  const origin = Math.max(0, currentSegment.origin);
  const destination = Math.max(0, currentSegment.destination);
  const connectorStyle = { "--progress": `${progressFraction}` } as CSSProperties & Record<"--progress", string>;

  useEffect(() => {
    if (isLive) return;
    const duration = 3600;
    let frame = 0;
    let startedAt = performance.now();
    const tick = (now: number) => {
      const elapsed = now - startedAt;
      setDemoFraction(Math.max(0, Math.min(1, elapsed / duration)));
      if (elapsed >= duration) {
        const current = visibleSegmentRef.current ?? fallbackSegment;
        const direction = (current.destination - current.origin + stations.length) % stations.length === 1 ? 1 : -1;
        const next = { origin: current.destination, destination: (current.destination + direction + stations.length) % stations.length };
        startWheelTransition(next);
        startedAt = now + 760;
      }
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [fallbackSegment, isLive, startWheelTransition]);

  return (
    <main className="journey" aria-describedby="journey-note">
      <div className="journey-layout">
      <div className={`rail-experience${showServiceStatusBanner ? " service-inactive" : ""}`}>
        <header className="journey-status">
          <MarketStatus status={marketStatus} />
          {showServiceStatusBanner && (
            <section className="service-status-banner" role="status" aria-live="polite" aria-label={resumeTime ? `The train is currently not running. It resumes at ${resumeTime}.` : `${serviceLabel}. No scheduled resumption time is available.`}>
              <p>{serviceLabel}</p>
              <p>{resumeTime ? `Resumes ${resumeTime}` : "No resume time available"}</p>
            </section>
          )}
        </header>
        <section className="station-stage" aria-label="Yamanote loop station explorer">
          <div className={`station-wheel${wheelTransition ? " station-wheel-moving" : ""}`} aria-live="polite">
            {wheelTransition ? (
              <>
                <StationCard index={wheelTransition.leaving} position="leaving" />
                <StationCard index={wheelTransition.origin} position="origin" />
                <StationCard index={wheelTransition.destination} position="destination" />
              </>
            ) : (
              <>
                <StationCard index={origin} position="origin" />
                <StationCard index={destination} position="destination" />
              </>
            )}
          </div>
          <div className={`rail-connector${isLive ? "" : " demo-connector"}`} role="progressbar" aria-label={isLive ? "Estimated section progress" : "Animated demo section progress"} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(progressFraction * 100)} aria-valuetext={progressDescription}>
            <div className="rail-track" aria-hidden="true" />
            <div className="rail-complete" aria-hidden="true" style={connectorStyle} />
            <div className="rail-endpoint rail-endpoint-start" aria-hidden="true" />
            <div className={`rail-endpoint rail-endpoint-end${progressFraction >= .99 ? " is-complete" : ""}`} aria-hidden="true" />
            <div className="rail-marker" aria-hidden="true" style={connectorStyle}><span>{Math.round(progressFraction * 100)}%</span></div>
          </div>
        </section>
        <MarketPanel serviceRunning={serviceRunning} />
        <footer className="footer-note" id="journey-note">
          {isLive ? <p><span className={`connection connection-${connection}`} aria-hidden="true" />{statusLabel} · {serviceLabel} · {trainLabel}. Progress is a section estimate, not GPS. Questions: {appContact}.</p> : <p>Demo mode · section progress is visual only.</p>}
          {isLive && snapshot.trains.length > 1 && (
            <label className="train-picker">
              <span>Selected live train</span>
              <select value={activeTrain?.id ?? ""} onChange={(event) => selectTrain(event.target.value)}>
                {snapshot.trains.map((train) => <option key={train.id} value={train.id}>{trainOptionLabel(train)}</option>)}
              </select>
            </label>
          )}
        </footer>
      </div>
      </div>
    </main>
  );
}

function StationCard({ index, position }: { index: number; position: "leaving" | "origin" | "destination" }) {
  const isOrigin = position === "origin";
  const headingId = isOrigin ? "origin-station" : position === "destination" ? "destination-station" : undefined;
  return (
    <section className={`station-card wheel-card wheel-card-${position}`} aria-hidden={position === "leaving" || undefined} aria-labelledby={headingId}>
      <div className="station-badge" aria-label={`Station code ${stationCodes[index]}`}><span>JY</span><b>{stationCodes[index].slice(2)}</b></div>
      {isOrigin ? <h1 className="station-name" id={headingId}>{stations[index]}</h1> : <h2 className="station-name" id={headingId}>{stations[index]}</h2>}
    </section>
  );
}
