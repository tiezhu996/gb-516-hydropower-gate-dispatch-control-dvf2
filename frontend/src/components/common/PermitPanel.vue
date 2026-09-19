
<script setup lang="ts">
import { computed, ref } from 'vue';
import { listGateDispatchPermits, applyGateDispatchPermit, issueGateDispatchPermit, revokeGateDispatchPermit } from '../../api/gate-dispatch-permit';
import type { DomainRecord, GateDispatchPermit } from '../../types/domain';
import { useAuth } from '../../hooks/useAuth';
import { formatDate, permitStatusLabel } from '../../utils/format';
import PermitStatusBadge from './PermitStatusBadge.vue';

const props = defineProps<{ directive: DomainRecord }>();
const emit = defineEmits<{ changed: [] }>();
const { can } = useAuth();

const open = ref(false);
const loading = ref(false);
const error = ref('');
const permits = ref<GateDispatchPermit[]>([]);
const mode = ref<'' | 'apply' | 'issue' | 'revoke'>('');
const reason = ref('');
const validMinutes = ref(30);
const actingOn = ref<GateDispatchPermit | null>(null);

const latest = computed<GateDispatchPermit | undefined>(() => permits.value[0]);
const active = computed<GateDispatchPermit | undefined>(() => permits.value.find((item) => item.status === 'issued' && !item.expired));
const pending = computed<GateDispatchPermit | undefined>(() => permits.value.find((item) => item.status === 'pending'));
const canApply = computed(() => props.directive.status === 'approved' && !active.value && !pending.value && can('operator', 'admin'));
const canIssue = computed(() => Boolean(pending.value) && can('reviewer', 'admin'));
const canRevoke = computed(() => Boolean(active.value) && can('reviewer', 'admin'));

async function loadPermits(): Promise<void> {
  loading.value = true;
  error.value = '';
  try {
    const result = await listGateDispatchPermits(props.directive.code);
    permits.value = result.data;
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    loading.value = false;
  }
}

async function togglePanel(show: boolean): Promise<void> {
  open.value = show;
  mode.value = '';
  reason.value = '';
  actingOn.value = null;
  if (show) await loadPermits();
}

function startApply(): void {
  mode.value = 'apply';
  reason.value = '已批准指令申请限时闸门调度许可';
  validMinutes.value = 30;
}

function startIssue(permit: GateDispatchPermit): void {
  mode.value = 'issue';
  actingOn.value = permit;
  reason.value = '闸门无生效许可且指令已批准，限时签发';
}

function startRevoke(permit: GateDispatchPermit): void {
  mode.value = 'revoke';
  actingOn.value = permit;
  reason.value = '';
}

const reasonValid = computed(() => reason.value.trim().length >= 3);

async function submit(): Promise<void> {
  if (!reasonValid.value) return;
  loading.value = true;
  error.value = '';
  try {
    if (mode.value === 'apply') {
      await applyGateDispatchPermit({
        code: `GDP-${String(Date.now()).slice(-7)}`,
        directiveId: props.directive.id,
        gateCode: props.directive.relatedCode,
        validMinutes: validMinutes.value,
        requestReason: reason.value.trim(),
      });
    } else if (mode.value === 'issue' && actingOn.value) {
      await issueGateDispatchPermit(actingOn.value.id, actingOn.value.version, reason.value.trim());
    } else if (mode.value === 'revoke' && actingOn.value) {
      await revokeGateDispatchPermit(actingOn.value.id, actingOn.value.version, reason.value.trim());
    }
    mode.value = '';
    await loadPermits();
    emit('changed');
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <el-popover :width="460" placement="bottom-start" trigger="click" @show="togglePanel(true)" @hide="togglePanel(false)">
    <template #reference>
      <el-button link type="primary" size="small">
        调度许可<PermitStatusBadge v-if="latest" class="permit-cell-badge" :status="latest.displayStatus" :expired="latest.expired" />
      </el-button>
    </template>
    <section class="permit-panel" v-loading="loading">
      <header class="permit-panel__head">
        <strong>{{ directive.code }} · {{ directive.relatedCode }}</strong>
        <small>闸门调度限时许可</small>
      </header>
      <el-alert v-if="error" :title="error" type="error" show-icon :closable="false" />
      <p v-if="!permits.length && !loading" class="muted permit-empty">该指令暂无许可记录；执行前必须持有生效许可。</p>
      <article v-for="permit in permits" :key="permit.id" class="permit-card">
        <div class="permit-card__row">
          <strong>{{ permit.code }}</strong>
          <PermitStatusBadge :status="permit.displayStatus" :expired="permit.expired" />
        </div>
        <dl class="permit-card__grid">
          <div><dt>有效期</dt><dd>{{ formatDate(permit.validFrom) }} 至 {{ formatDate(permit.validUntil) }}</dd></div>
          <div><dt>申请人</dt><dd>{{ permit.requestedBy }} · {{ formatDate(permit.requestedAt) }}</dd></div>
          <div v-if="permit.issuedBy"><dt>签发人</dt><dd>{{ permit.issuedBy }} · {{ formatDate(permit.issuedAt || '') }}</dd></div>
          <div v-if="permit.issueReason"><dt>签发意见</dt><dd>{{ permit.issueReason }}</dd></div>
          <div v-if="permit.revokeReason"><dt>撤销原因</dt><dd class="permit-danger">{{ permit.revokeReason }}</dd></div>
          <div v-if="permit.status === 'expired' || permit.expired"><dt>过期状态</dt><dd class="permit-danger">已超过有效期，禁止执行</dd></div>
          <div v-if="permit.closeReason && permit.status !== 'revoked'"><dt>关闭原因</dt><dd>{{ permit.closeReason }}</dd></div>
          <div><dt>申请理由</dt><dd>{{ permit.requestReason }}</dd></div>
        </dl>
        <div v-if="permit.status === 'pending'" class="permit-card__actions">
          <el-button v-if="canIssue" link type="primary" size="small" @click="startIssue(permit)">签发限时许可</el-button>
          <span v-else class="muted">等待复核员签发</span>
        </div>
        <div v-if="permit.status === 'issued' && !permit.expired" class="permit-card__actions">
          <el-button v-if="canRevoke" link type="danger" size="small" @click="startRevoke(permit)">撤销许可</el-button>
          <span v-else class="muted">许可生效中（{{ permitStatusLabel(permit.displayStatus) }}）</span>
        </div>
      </article>
      <div v-if="mode === 'apply'" class="permit-form">
        <el-select v-model="validMinutes" size="small" aria-label="许可有效时长">
          <el-option :value="15" label="15 分钟" /><el-option :value="30" label="30 分钟" />
          <el-option :value="60" label="1 小时" /><el-option :value="120" label="2 小时" />
        </el-select>
        <el-input v-model="reason" type="textarea" :rows="2" maxlength="500" show-word-limit placeholder="申请原因（3-500 字）" />
        <div class="permit-form__actions">
          <el-button size="small" @click="mode = ''">取消</el-button>
          <el-button size="small" type="primary" :disabled="!reasonValid" @click="submit">提交申请</el-button>
        </div>
      </div>
      <div v-else-if="mode === 'issue' || mode === 'revoke'" class="permit-form">
        <el-input v-model="reason" type="textarea" :rows="2" maxlength="500" show-word-limit
          :placeholder="mode === 'issue' ? '签发意见（3-500 字）' : '撤销原因（3-500 字）'" />
        <div class="permit-form__actions">
          <el-button size="small" @click="mode = ''">取消</el-button>
          <el-button size="small" :type="mode === 'issue' ? 'primary' : 'danger'" :disabled="!reasonValid" @click="submit">
            {{ mode === 'issue' ? '确认签发' : '确认撤销' }}
          </el-button>
        </div>
      </div>
      <footer v-else-if="!loading" class="permit-panel__foot">
        <el-button v-if="canApply" size="small" type="primary" @click="startApply">申请限时许可</el-button>
        <span v-else-if="!canIssue && !canRevoke" class="muted">{{ permitStatusLabel(latest?.displayStatus || 'pending') }}</span>
      </footer>
    </section>
  </el-popover>
</template>

<style scoped>
.permit-panel { display: grid; gap: 10px; max-height: 440px; overflow-y: auto; }
.permit-panel__head { display: grid; gap: 2px; }
.permit-panel__head small { color: #7d8d97; }
.permit-empty { margin: 4px 0; font-size: 12px; }
.permit-card { border: 1px solid #dbe4e8; border-left: 3px solid #2a9d78; padding: 10px; display: grid; gap: 8px; }
.permit-card__row { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.permit-card__grid { display: grid; gap: 6px; margin: 0; }
.permit-card__grid dt { color: #778994; font-size: 11px; }
.permit-card__grid dd { margin: 2px 0 0; font-size: 12px; line-height: 1.5; word-break: break-word; }
.permit-danger { color: #9d2c37; font-weight: 600; }
.permit-card__actions { display: flex; justify-content: flex-end; }
.permit-form { display: grid; gap: 8px; border-top: 1px dashed #cbd7dc; padding-top: 10px; }
.permit-form__actions { display: flex; justify-content: flex-end; gap: 8px; }
.permit-panel__foot { display: flex; justify-content: flex-end; }
.permit-cell-badge { margin-left: 6px; }
</style>
