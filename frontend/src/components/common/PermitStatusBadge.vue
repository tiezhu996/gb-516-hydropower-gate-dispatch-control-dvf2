
<script setup lang="ts">
import { computed } from 'vue';
import { permitStatusLabel, permitStatusTone } from '../../utils/format';

const props = defineProps<{ status: string; expired?: boolean }>();
const tone = computed(() => (props.expired && props.status === 'issued' ? 'danger' : permitStatusTone(props.status)));
const label = computed(() => permitStatusLabel(props.expired && props.status === 'issued' ? 'expired' : props.status));
</script>
<template><span :class="`status status--${tone}`">{{ label }}</span></template>
