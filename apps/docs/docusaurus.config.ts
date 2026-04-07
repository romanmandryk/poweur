import { themes as prismThemes } from 'prism-react-renderer';
import type { Config } from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'Eurything Protocol',
  tagline: 'Open, DNS-native identity and messaging protocol',
  favicon: 'img/favicon.ico',

  // Production URL — deploy standalone
  url: 'https://docs.eurything.com',
  baseUrl: '/',

  organizationName: 'eurything',
  projectName: 'eurything',

  onBrokenLinks: 'warn',
  onBrokenMarkdownLinks: 'warn',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  markdown: {
    mermaid: true,
  },

  themes: ['@docusaurus/theme-mermaid'],

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
          editUrl: 'https://github.com/eurything/eurything/edit/main/apps/docs/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/social-card.png',
    navbar: {
      title: 'Eurything Protocol',
      logo: {
        alt: 'Eurything Logo',
        src: 'img/logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Docs',
        },
        {
          href: '/relay/api-reference',
          label: 'API Reference',
          position: 'left',
        },
        {
          label: 'Get Started',
          href: '/',
          position: 'right',
          className: 'navbar__item--cta',
        },
        {
          href: '#',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Protocol',
          items: [
            { label: 'Introduction', to: '/' },
            { label: 'Protocol Overview', to: '/protocol/overview' },
            { label: 'Identity Model', to: '/protocol/identity-model' },
            { label: 'DNS Records', to: '/protocol/dns-records' },
          ],
        },
        {
          title: 'Relay',
          items: [
            { label: 'Relay Overview', to: '/relay/overview' },
            { label: 'API Reference', to: '/relay/api-reference' },
            { label: 'Configuration', to: '/relay/configuration' },
          ],
        },
        {
          title: 'More',
          items: [
            { label: 'Security Model', to: '/security/model' },
            { label: 'Future Capabilities', to: '/future/capabilities' },
            { label: 'GitHub', href: '#' },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Eurything. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'toml', 'dns-zone-file'],
    },
    mermaid: {
      theme: { light: 'neutral', dark: 'dark' },
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
