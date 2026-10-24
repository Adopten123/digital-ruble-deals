import { useCallback, useEffect, useRef, useState } from 'react';
import { api } from './lib/api';
import type { Notification } from './lib/api';
import { isAborted, NotificationReadGate, notificationQuery } from './lib/notifications';
import type { NotificationFilter } from './lib/notifications';

export default function useNotifications({ userId, enabled, active, onUnauthorized }: {
  userId: string; enabled: boolean; active: boolean; onUnauthorized: (error: unknown) => void;
}) {
  const [unreadCount, setUnreadCount] = useState<number | null>(null);
  const [filter, setFilter] = useState<NotificationFilter>('all');
  const [items, setItems] = useState<Notification[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [pollError, setPollError] = useState<unknown>(null);
  const [readError, setReadError] = useState<unknown>(null);
  const [reading, setReading] = useState<string | null>(null);
  const [uncertainId, setUncertainId] = useState<string | null>(null);
  const gate = useRef(new NotificationReadGate());
  const mounted = useRef(false);
  const mutation = useRef<AbortController | null>(null);
  const state = useRef({ active, filter, items }); state.current = { active, filter, items };
  const alive = useCallback(() => mounted.current && enabled && api.currentIdentity() === userId, [enabled, userId]);

  const read = useCallback(async (mode: 'badge' | 'feed' | 'background', selected: NotificationFilter, next?: string) => {
    if (!alive()) return;
    const ticket = gate.current.begin();
    if (mode === 'feed') { setLoading(true); setError(null); if (!next) { setItems([]); setCursor(null); } }
    const current = () => alive() && gate.current.accepts(ticket);
    try {
      const page = await api.notifications(mode === 'badge' ? { limit: 1 } : notificationQuery(selected), next, ticket.signal);
      if (!current()) return;
      setUnreadCount(page.unread_count); setPollError(null);
      if (mode !== 'badge') { setItems(previous => next ? [...previous, ...page.items] : page.items); setCursor(page.next_cursor); }
    } catch (failure) {
      if (current() && !isAborted(failure)) {
        if (mode === 'feed') setError(failure); else setPollError(failure);
        onUnauthorized(failure);
      }
    } finally { if (current() && mode === 'feed') setLoading(false); gate.current.finish(ticket); }
  }, [alive, onUnauthorized]);
  const refresh = useCallback(() => {
    if (mutation.current) return;
    void read('feed', state.current.filter);
  }, [read]);
  const poll = useCallback(() => {
    if (!alive() || gate.current.busy || mutation.current) return;
    const current = state.current;
    void read(current.active && current.items.length <= 20 ? 'background' : 'badge', current.filter);
  }, [alive, read]);
  useEffect(() => {
    if (!enabled) return;
    mounted.current = true; poll();
    const timer = setInterval(poll, 15_000);
    window.addEventListener('focus', poll);
    return () => { mounted.current = false; clearInterval(timer); window.removeEventListener('focus', poll); gate.current.cancel(); mutation.current?.abort(); mutation.current = null; };
  }, [enabled, poll]);
  const wasActive = useRef(false);
  useEffect(() => {
    if (active && enabled) void read('feed', filter);
    else if (wasActive.current) { gate.current.cancel(); setLoading(false); }
    wasActive.current = active;
  }, [active, enabled, filter, read]);

  async function markRead(id: string) {
    if (!alive() || mutation.current) return;
    const controller = new AbortController(); mutation.current = controller;
    gate.current.cancel(); setLoading(false); setReading(id); setReadError(null);
    try {
      await api.markNotificationRead(id, controller.signal);
      if (!alive() || controller.signal.aborted) return;
      setUncertainId(null);
      await read(state.current.active ? 'feed' : 'badge', state.current.filter);
    } catch (failure) {
      if (alive() && !controller.signal.aborted && !isAborted(failure)) {
        setReadError(failure); setUncertainId(id); onUnauthorized(failure);
      }
    } finally { if (mutation.current === controller) { mutation.current = null; if (alive()) setReading(null); } }
  }
  return { unreadCount, filter, setFilter, items, cursor, loading, error, pollError, readError, reading, uncertainId,
    refresh, more: () => { if (cursor && !loading && !mutation.current) void read('feed', state.current.filter, cursor); }, markRead };
}
