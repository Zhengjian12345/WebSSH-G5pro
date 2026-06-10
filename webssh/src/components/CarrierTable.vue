<template>
  <div class="card">
    <div class="card-header">
      <h3 class="hd">
        <img style="width: 24px" :src="NetworkIcon" alt="" />{{ title }}
      </h3>
      <span class="tag success">{{ tagText }}</span>
    </div>
    <div class="card-content">
      <div class="signal-grid">
        <table class="mytable" width="100%">
          <thead>
            <tr>
              <td width="13%"></td>
              <td width="9%">PCI</td>
              <td width="11%">{{ frequencyLabel }}</td>
              <td width="16%">{{ channelLabel }}</td>
              <td width="11%">{{ bandwidthLabel }}</td>
              <td width="10%">RSRP</td>
              <td width="10%">RSRQ</td>
              <td width="10%">SINR</td>
              <td width="10%">RSSI</td>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, idx) in tableRows" :key="idx">
              <td>{{ row.label }}</td>
              <td v-for="(cell, cidx) in row.cells" :key="cidx" :class="cell.class">
                {{ cell.value }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script lang="ts">
import { defineComponent, PropType } from 'vue';

interface TableCell {
  value: string | number;
  class?: string[];
}

interface TableRow {
  label: string;
  cells: TableCell[];
}

export default defineComponent({
  name: 'CarrierTable',
  props: {
    title: {
      type: String,
      required: true,
    },
    tagText: {
      type: String,
      required: true,
    },
    frequencyLabel: {
      type: String,
      default: '频段',
    },
    channelLabel: {
      type: String,
      default: '频点',
    },
    bandwidthLabel: {
      type: String,
      default: '带宽',
    },
    tableRows: {
      type: Array as PropType<TableRow[]>,
      required: true,
    },
    NetworkIcon: {
      type: String,
      required: true,
    },
  },
});
</script>

<style scoped>
/* Styles inherited from parent */
</style>
