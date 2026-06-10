<template>
  <div class="signal-item">
    <div class="signal-label-row">
      <span class="signal-label-help">
        <span class="label">{{ label }}</span>
        <button
          type="button"
          class="signal-help-trigger"
          :aria-expanded="isOpen"
          @click="toggleHelp"
        >*</button>
      </span>
      <span :class="['signal-status', displayStatus.className]">
        {{ displayStatus.text }}
      </span>
    </div>
    <div class="progress-bar">
      <div
        class="progress-fill"
        :class="displayStatus.className"
        :style="{ width: percentage + '%' }"
      ></div>
      <span class="progress-text">{{ formattedValue }}</span>
    </div>
    <div v-if="isOpen" class="signal-help-panel">
      <div class="signal-help-title">{{ helpInfo.title }}</div>
      <div class="signal-help-desc">{{ helpInfo.description }}</div>
      <div class="signal-help-ranges">
        <div v-for="item in helpInfo.ranges" :key="item.label">
          <span :class="['signal-help-dot', item.className]"></span>
          <span>{{ item.label }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script lang="ts">
import { defineComponent, PropType } from 'vue';

interface SignalHelpInfo {
  title: string;
  description: string;
  ranges: Array<{ label: string; className: string }>;
}

interface SignalDisplayStatus {
  text: string;
  className: string;
}

export default defineComponent({
  name: 'SignalItem',
  props: {
    label: {
      type: String,
      required: true,
    },
    value: {
      type: [Number, String],
      required: true,
    },
    percentage: {
      type: Number,
      default: 0,
    },
    formattedValue: {
      type: String,
      required: true,
    },
    displayStatus: {
      type: Object as PropType<SignalDisplayStatus>,
      required: true,
    },
    helpInfo: {
      type: Object as PropType<SignalHelpInfo>,
      required: true,
    },
    isOpen: {
      type: Boolean,
      default: false,
    },
  },
  emits: ['toggle-help'],
  methods: {
    toggleHelp() {
      this.$emit('toggle-help');
    },
  },
});
</script>

<style scoped>
/* Styles inherited from parent */
</style>
