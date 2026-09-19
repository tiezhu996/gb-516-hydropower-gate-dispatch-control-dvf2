import { defineStore } from 'pinia';
import {
  applyDispatchPermit,
  issueDispatchPermit,
  listDispatchPermits,
  revokeDispatchPermit,
} from '../api/dispatch-permit';
import type { DispatchPermit } from '../types/domain';

// DispatchPermitStore keeps the signed/queued permits aligned with the
// directive workbench. Items are keyed by directive so a row can surface its
// effective window, issuer, revocation reason and expiry state.
export const useDispatchPermitStore = defineStore('dispatchPermit', {
  state: () => ({
    items: [] as DispatchPermit[],
    loading: false,
    error: '',
  }),
  getters: {
    // Most operationally-relevant permit per directive: active wins, then
    // pending, then the freshest terminal record.
    byDirective: (state): Record<number, DispatchPermit> => {
      const map: Record<number, DispatchPermit> = {};
      const rank: Record<string, number> = { active: 5, pending: 4, expired: 3, revoked: 2, invalidated: 1 };
      for (const permit of state.items) {
        const existing = map[permit.directiveId];
        if (!existing || (rank[permit.status] ?? 0) > (rank[existing.status] ?? 0)) {
          map[permit.directiveId] = permit;
        }
      }
      return map;
    },
  },
  actions: {
    async load(directiveCode?: string, gateCode?: string) {
      this.loading = true;
      this.error = '';
      try {
        const result = await listDispatchPermits({ pageSize: 100, directiveCode, gateCode });
        this.items = result.data;
      } catch (error) {
        this.error = error instanceof Error ? error.message : String(error);
      } finally {
        this.loading = false;
      }
    },
    async apply(input: { directiveCode: string; gateCode?: string; durationMinutes: number; reason: string }) {
      this.loading = true;
      this.error = '';
      try {
        await applyDispatchPermit(input);
        await this.load();
      } catch (error) {
        this.error = error instanceof Error ? error.message : String(error);
        throw error;
      } finally {
        this.loading = false;
      }
    },
    async issue(id: number, expectedVersion: number, reason: string) {
      this.loading = true;
      this.error = '';
      try {
        await issueDispatchPermit(id, expectedVersion, reason);
        await this.load();
      } catch (error) {
        this.error = error instanceof Error ? error.message : String(error);
        throw error;
      } finally {
        this.loading = false;
      }
    },
    async revoke(id: number, reason: string) {
      this.loading = true;
      this.error = '';
      try {
        await revokeDispatchPermit(id, reason);
        await this.load();
      } catch (error) {
        this.error = error instanceof Error ? error.message : String(error);
        throw error;
      } finally {
        this.loading = false;
      }
    },
  },
});
