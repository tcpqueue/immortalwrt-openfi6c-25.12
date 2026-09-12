"""Exercise the prepared driver's RX dispatch with real descriptor definitions."""
from pathlib import Path
import os
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
KERNEL = next((ROOT / 'build_dir/target-aarch64_cortex-a53_musl/linux-mediatek_filogic').glob('linux-6.12.*'))
DRIVER = KERNEL / 'drivers/net/ethernet/mediatek'
source = Path(os.environ.get('HNAT_TEST_SOURCE', DRIVER / 'mtk_eth_soc.c')).read_text()
header = (DRIVER / 'mtk_eth_soc.h').read_text()
ppe = (DRIVER / 'mtk_ppe.h').read_text()
hnat = (DRIVER / 'mtk_hnat/nf_hnat_mtk.h').read_text()
ports = re.search(r'enum mtk_pse_port \{.*?\n\};', header, re.S).group()
reasons = re.search(r'enum \{\s*MTK_PPE_CPU_REASON_TTL_EXCEEDED.*?\n\};', ppe, re.S).group()
desc = hnat.split('#else\nstruct hnat_desc {', 1)[1].split('} __packed;', 1)[0]
defines = '\n'.join(line for line in header.splitlines() if any(line.startswith('#define ' + key + '\t') or line.startswith('#define ' + key + '(') for key in
    ('MTK_RXD4_PPE_CPU_REASON', 'MTK_RXD5_PPE_CPU_REASON', 'RX_DMA_GET_SPORT', 'RX_DMA_GET_SPORT_V2', 'RX_DMA_SPECIAL_TAG', 'MTK_MAX_DEVS')))
poll = source[source.index('static int mtk_poll_rx'):]
start = poll.index('\t\t/* ', poll.index('if (!mtk_rx_get_desc'))
dispatch = poll[start:poll.index('\n\t\ttops_crsn =', start)]
helpers = ''
if 'static bool mtk_is_rx_ppd' in source:
    helpers = source[source.index('static bool mtk_is_rx_ppd'):source.index('static int mtk_poll_rx')]
program = r'''
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
typedef uint32_t u32;
#define BIT(n) (1U << (n))
#define GENMASK(h,l) ((~0U >> (31-(h))) & (~0U << (l)))
#define FIELD_GET(mask,v) (((v) & (mask)) >> __builtin_ctz(mask))
#define unlikely(x) (x)
#define MTK_GMAC3_ID 2
#define MTK_SOC_MT7628 1
#define MTK_HAS_CAPS(caps,bit) ((caps) & (bit))
struct net_device { int unused; };
struct mtk_soc { int version; unsigned int caps; };
struct mtk_eth { struct mtk_soc *soc; struct net_device *netdev[3]; };
struct rx_desc { u32 rxd4; u32 rxd5; };
static bool mtk_is_netsys_v3_or_greater(struct mtk_eth *eth) { return eth->soc->version > 2; }
static bool mtk_is_netsys_v2_or_greater(struct mtk_eth *eth) { return eth->soc->version > 1; }
'''
program += ports + '\n' + reasons + '\n' + defines + '\nstruct hnat_desc {' + desc + '} __attribute__((packed));\n' + helpers
program += '''
static int select_rx(struct mtk_eth *eth, struct rx_desc trxd, struct net_device *rx_ppdev, bool *redirect) {
    int mac = 0;
    u32 sport = 0, reason = 0;
    bool to_ppd = false;
    (void)sport; (void)reason;
'''+dispatch+'''
    *redirect = to_ppd;
    return mac;
release_desc:
    *redirect = false;
    return -1;
}
'''
program += r'''
static unsigned int failures, checks;
static void check(struct mtk_eth *eth, unsigned int port, unsigned int reason,
                  bool ppd, int expected, bool expect_redirect) {
    struct net_device dummy;
    struct rx_desc raw = {0};
    union { struct hnat_desc desc; u32 word; } h = {0};
    bool redirect = false;
    if (eth->soc->version == 2) {
        h.desc.sport = port; h.desc.crsn = reason;
        raw.rxd4 = h.word;
    } else {
        raw.rxd5 = (port << 26) | (reason << 18);
    }
    int result = select_rx(eth, raw, ppd ? &dummy : NULL, &redirect);
    checks++;
    if (result != expected || redirect != expect_redirect) {
        fprintf(stderr, "v%d sport=%u reason=%u expected=%d/%d actual=%d/%d\n",
            eth->soc->version, port, reason, expected, expect_redirect, result, redirect);
        failures++;
    }
}
int main(void) {
    struct net_device dev[3];
    struct mtk_soc soc = {0};
    struct mtk_eth eth = {.soc = &soc};
    unsigned int ports[] = {PSE_QDMA_TX_PORT, PSE_WDMA0_PORT, PSE_WDMA1_PORT};
    for (soc.version = 2; soc.version <= 3; soc.version++) {
        for (unsigned int mask = 0; mask < 8; mask++) {
            int first = -1;
            for (int i = 0; i < 3; i++) {
                eth.netdev[i] = (mask & (1U << i)) ? &dev[i] : NULL;
                if (eth.netdev[i] && first == -1) first = i;
            }
            for (unsigned int p = 0; p < sizeof(ports)/sizeof(ports[0]); p++) {
                check(&eth, ports[p], MTK_PPE_CPU_REASON_HIT_BIND_FORCE_CPU, true, first, first >= 0);
                check(&eth, ports[p], MTK_PPE_CPU_REASON_HIT_BIND_FORCE_CPU, false, -1, false);
            }
            check(&eth, PSE_QDMA_TX_PORT, MTK_PPE_CPU_REASON_HIT_UNBIND, true, first, first >= 0);
            check(&eth, PSE_GDM1_PORT, MTK_PPE_CPU_REASON_HIT_BIND_FORCE_CPU, true, (mask & 1) ? 0 : -1, false);
            check(&eth, PSE_GDM2_PORT, MTK_PPE_CPU_REASON_HIT_BIND_FORCE_CPU, true, (mask & 2) ? 1 : -1, false);
            if (soc.version == 3) {
                check(&eth, PSE_GDM3_PORT, MTK_PPE_CPU_REASON_HIT_UNBIND, true, (mask & 4) ? 2 : -1, false);
                check(&eth, PSE_WDMA2_PORT, MTK_PPE_CPU_REASON_HIT_BIND_FORCE_CPU, true, first, first >= 0);
            }
        }
    }
    printf("RX dispatch: %u cases, %u failures\n", checks, failures);
    return failures ? 1 : 0;
}
'''
with tempfile.TemporaryDirectory(dir=ROOT / '.build-local') as tmp:
    path = Path(tmp)
    (path / 'test.c').write_text(program)
    subprocess.run(['cc', '-std=gnu11', '-Wall', '-Wextra', '-Wno-unused-function', '-fsanitize=undefined', '-o', str(path / 'test'), str(path / 'test.c')], check=True)
    subprocess.run([str(path / 'test')], check=True)
