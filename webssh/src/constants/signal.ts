/**
 * Constants for network signal analysis
 */

export const NETWORK_TYPES = {
  '5G': '5G',
  '4G': '4G',
} as const;

export const SIGNAL_TYPES = {
  NR: 'nr',
  LTE: 'lte',
} as const;

export const SIGNAL_METRICS = {
  RSRP: 'rsrp',
  RSRQ: 'rsrq',
  SINR: 'sinr',
  RSSI: 'rssi',
} as const;

/**
 * Signal quality thresholds and classifications
 */
export const SIGNAL_THRESHOLDS = {
  rsrp: {
    excellent: -100,
    good: -110,
    fair: -120,
    poor: -130,
  },
  rsrq: {
    excellent: -5,
    good: -10,
    fair: -15,
    poor: -20,
  },
  sinr: {
    excellent: 15,
    good: 10,
    fair: 5,
    poor: 0,
  },
  rssi: {
    excellent: -60,
    good: -75,
    fair: -85,
    poor: -100,
  },
} as const;

/**
 * Carrier aggregation configuration
 */
export const CARRIER_CONFIG = {
  '5G': {
    frequencyLabel: '频段',
    channelLabel: '频点',
    maxCarriers: 3,
    bandPrefix: 'N',
  },
  '4G': {
    frequencyLabel: '频段',
    channelLabel: '信道',
    maxCarriers: 4,
    bandPrefix: 'B',
  },
} as const;

export const CARRIER_LABELS = ['PCC', 'SCC0', 'SCC1', 'SCC2', 'SCC3'] as const;
