import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Mic, Square } from "lucide-react";

import { Button } from "@/components/ui/button";

type Status = "idle" | "connecting" | "connected";
type Session = { socket?: WebSocket; stream?: MediaStream; context?: AudioContext; processor?: ScriptProcessorNode; source?: MediaStreamAudioSourceNode; playing: Set<AudioBufferSourceNode>; nextPlayback: number; stopped: boolean; suppressAudio: boolean };

export function RealtimeVoice({ apiKey, model }: { apiKey: string; model: string }) {
  const { t } = useTranslation();
  const [status, setStatus] = useState<Status>("idle");
  const [error, setError] = useState("");
  const [transcript, setTranscript] = useState<string[]>([]);
  const sessionRef = useRef<Session | null>(null);

  function dispose(session: Session) {
    if (session.stopped) return;
    session.stopped = true;
    session.processor?.disconnect();
    session.source?.disconnect();
    session.stream?.getTracks().forEach((track) => track.stop());
    for (const source of session.playing) { try { source.stop(); } catch { /* already stopped */ } }
    session.playing.clear();
    if (session.socket && session.socket.readyState < WebSocket.CLOSING) session.socket.close();
    if (session.context && session.context.state !== "closed") void session.context.close();
    if (sessionRef.current === session) sessionRef.current = null;
  }

  useEffect(() => () => { if (sessionRef.current) dispose(sessionRef.current); }, [apiKey, model]);

  function stop() {
    if (sessionRef.current) dispose(sessionRef.current);
    setStatus("idle");
  }

  async function start() {
    if (sessionRef.current || !apiKey || !model) return;
    const session: Session = { playing: new Set(), nextPlayback: 0, stopped: false, suppressAudio: false };
    sessionRef.current = session;
    setError(""); setTranscript([]); setStatus("connecting");
    try {
      if (!navigator.mediaDevices?.getUserMedia) throw new Error(t("creativeConsole.realtimeUnavailable"));
      session.stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
      if (session.stopped) { session.stream.getTracks().forEach((track) => track.stop()); return; }
      const context = new AudioContext({ sampleRate: 24000 });
      session.context = context;
      await context.resume();
      if (session.stopped) return;
      if (![8000, 16000, 22050, 24000, 32000, 44100, 48000].includes(context.sampleRate)) throw new Error(t("creativeConsole.realtimeAudioRate"));
      const url = new URL("/v1/realtime", window.location.href);
      url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
      url.searchParams.set("model", model);
      const socket = new WebSocket(url, ["grok2api-realtime", `grok2api-key.${apiKey}`]);
      session.socket = socket;
      socket.onopen = () => {
        if (session.stopped) return;
        socket.send(JSON.stringify({ type: "session.update", session: { voice: "eve", turn_detection: { type: "server_vad" }, audio: { input: { format: { type: "audio/pcm", rate: context.sampleRate } }, output: { format: { type: "audio/pcm", rate: context.sampleRate } } } } }));
      };
      socket.onmessage = (message) => {
        if (session.stopped || typeof message.data !== "string") return;
        let event: Record<string, unknown>;
        try { event = JSON.parse(message.data) as Record<string, unknown>; } catch { return; }
        if (event.type === "session.updated" && !session.processor) {
          const source = context.createMediaStreamSource(session.stream!);
          const processor = context.createScriptProcessor(4096, 1, 1);
          session.source = source; session.processor = processor;
          processor.onaudioprocess = (audioEvent) => {
            if (socket.readyState !== WebSocket.OPEN || session.stopped || socket.bufferedAmount > 1024 * 1024) return;
            const samples = audioEvent.inputBuffer.getChannelData(0);
            const bytes = new Uint8Array(samples.length * 2);
            const view = new DataView(bytes.buffer);
            for (let i = 0; i < samples.length; i++) { const value = Math.max(-1, Math.min(1, samples[i])); view.setInt16(i * 2, value < 0 ? value * 32768 : value * 32767, true); }
            let binary = ""; for (const byte of bytes) binary += String.fromCharCode(byte);
            socket.send(JSON.stringify({ type: "input_audio_buffer.append", audio: btoa(binary) }));
          };
          source.connect(processor); processor.connect(context.destination);
          setStatus("connected");
        } else if ((event.type === "response.output_audio.delta" || event.type === "response.audio.delta") && typeof event.delta === "string") {
          if (session.suppressAudio) return;
          const binary = atob(event.delta);
          const buffer = context.createBuffer(1, Math.floor(binary.length / 2), context.sampleRate);
          const channel = buffer.getChannelData(0);
          for (let i = 0; i < channel.length; i++) { const value = binary.charCodeAt(i * 2) | (binary.charCodeAt(i * 2 + 1) << 8); channel[i] = (value >= 32768 ? value - 65536 : value) / 32768; }
          const player = context.createBufferSource(); player.buffer = buffer; player.connect(context.destination);
          session.playing.add(player); player.onended = () => session.playing.delete(player);
          session.nextPlayback = Math.max(context.currentTime, session.nextPlayback); player.start(session.nextPlayback); session.nextPlayback += buffer.duration;
        } else if (event.type === "input_audio_buffer.speech_started") {
          session.suppressAudio = true;
          for (const player of session.playing) { try { player.stop(); } catch { /* already stopped */ } }
          session.playing.clear(); session.nextPlayback = context.currentTime;
          if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "response.cancel" }));
        } else if (event.type === "response.created") {
          session.suppressAudio = false;
        } else if ((event.type === "conversation.item.input_audio_transcription.completed" || event.type === "response.output_audio_transcript.done" || event.type === "response.audio_transcript.done") && typeof event.transcript === "string") {
          const label = event.type.startsWith("conversation.") ? t("creativeConsole.realtimeYou") : t("creativeConsole.realtimeAssistant");
          setTranscript((previous) => [...previous.slice(-49), `${label}: ${event.transcript as string}`]);
        } else if (event.type === "error") {
          const detail = event.error && typeof event.error === "object" ? (event.error as { message?: unknown }).message : undefined;
          setError(typeof detail === "string" ? detail : t("creativeConsole.realtimeError"));
          dispose(session); setStatus("idle");
        }
      };
      socket.onerror = () => { if (!session.stopped) setError(t("creativeConsole.realtimeError")); };
      socket.onclose = () => { if (!session.stopped) { setError(t("creativeConsole.realtimeDisconnected")); dispose(session); setStatus("idle"); } };
    } catch (cause) {
      if (!session.stopped) { setError(cause instanceof Error ? cause.message : t("creativeConsole.realtimeError")); dispose(session); setStatus("idle"); }
    }
  }

  return <div className="space-y-3 px-4 py-4">
    <div className="text-sm text-muted-foreground" role="status">{t(`creativeConsole.realtimeStatus.${status}`)}</div>
    {transcript.length > 0 ? <div className="max-h-48 space-y-2 overflow-y-auto text-sm" aria-live="polite">{transcript.map((line, index) => <p key={index}>{line}</p>)}</div> : null}
    {error ? <p className="text-xs text-destructive" role="alert">{error}</p> : null}
    {status === "idle" ? <Button type="button" disabled={!apiKey || !model} onClick={() => void start()}><Mic />{t("creativeConsole.realtimeStart")}</Button> : <Button type="button" variant="secondary" onClick={stop}><Square />{t("creativeConsole.realtimeStop")}</Button>}
  </div>;
}
