import type { NotificationQuery } from './api.ts';

export type NotificationFilter = 'all' | 'unread' | 'read';
export const notificationFilters: Record<NotificationFilter, string> = { all: 'Всё', unread: 'Непрочитанные', read: 'Прочитанные' };
export function notificationQuery(filter: NotificationFilter): NotificationQuery {
  return filter === 'all' ? {} : { unread: filter === 'unread' };
}
export function isAborted(error: unknown): boolean { return error instanceof Error && error.name === 'AbortError'; }
export interface NotificationRead { revision: number; signal: AbortSignal }

/** Every explicit read replaces its predecessor; background polling skips a busy gate. */
export class NotificationReadGate {
  private revision = 0;
  private controller: AbortController | null = null;
  get busy(): boolean { return this.controller !== null; }
  begin(): NotificationRead {
    this.cancel(); this.controller = new AbortController();
    return { revision: this.revision, signal: this.controller.signal };
  }
  accepts(ticket: NotificationRead): boolean { return this.controller !== null && ticket.revision === this.revision && !ticket.signal.aborted; }
  finish(ticket: NotificationRead): void { if (ticket.revision === this.revision) this.controller = null; }
  cancel(): void { this.revision += 1; this.controller?.abort(); this.controller = null; }
}
