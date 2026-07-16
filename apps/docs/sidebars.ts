import type { SidebarsConfig } from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docsSidebar: [
    {
      type: 'doc',
      id: 'intro',
      label: 'Introduction',
    },
    {
      type: 'category',
      label: 'Protocol',
      collapsed: false,
      items: [
        'protocol/overview',
        'protocol/identity-model',
        'protocol/web-identity',
        'protocol/dns-records',
        'protocol/message-format',
        'protocol/routing',
        'protocol/delivery-acks',
        'protocol/interoperability',
        'protocol/rate-limiting',
      ],
    },
    {
      type: 'category',
      label: 'Relay',
      collapsed: false,
      items: [
        'relay/overview',
        'relay/api-reference',
        'relay/configuration',
        'relay/dns-management',
      ],
    },
    {
      type: 'category',
      label: 'Files',
      collapsed: false,
      items: [
        'files/storage-model',
        'files/webdav',
        'files/e2ee-design',
      ],
    },
    {
      type: 'category',
      label: 'Clients',
      collapsed: false,
      items: [
        'clients/overview',
        'clients/cli-reference',
      ],
    },
    {
      type: 'category',
      label: 'Security',
      collapsed: false,
      items: [
        'security/model',
        'security/tls',
      ],
    },
    {
      type: 'category',
      label: 'Future',
      collapsed: true,
      items: [
        'future/capabilities',
      ],
    },
  ],
};

export default sidebars;
